// Package store is the encrypted, revisioned control-plane state boundary.
package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrConflict = errors.New("revision conflict")
var ErrNotFound = errors.New("object not found")
var ErrLimit = errors.New("catalogue_budget_exceeded")

type Object struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Version   int64           `json:"version"`
	UpdatedAt time.Time       `json:"updated_at"`
	Data      json.RawMessage `json:"data"`
}
type Event struct {
	ID       int64           `json:"id"`
	Time     time.Time       `json:"time"`
	Kind     string          `json:"kind"`
	ObjectID string          `json:"object_id"`
	Action   string          `json:"action"`
	Detail   json.RawMessage `json:"detail"`
}
type Store struct {
	DB   *pgxpool.Pool
	AEAD cipher.AEAD
}

func New(ctx context.Context, url, keyPath string) (*Store, error) {
	key, e := keyFile(keyPath)
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	c, e := pgxpool.ParseConfig(url)
	if e != nil {
		return nil, fmt.Errorf("invalid DATABASE_URL")
	}
	c.MaxConns = 16
	c.MinConns = 1
	c.ConnConfig.ConnectTimeout = 5 * time.Second
	db, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, fmt.Errorf("database unavailable")
	}
	s := &Store{DB: db, AEAD: aead}
	if e = db.Ping(ctx); e != nil {
		db.Close()
		return nil, fmt.Errorf("database unavailable")
	}
	_, e = db.Exec(ctx, `CREATE TABLE IF NOT EXISTS objects(kind text NOT NULL,id text NOT NULL,name text NOT NULL,version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),data bytea NOT NULL,PRIMARY KEY(kind,id));
CREATE INDEX IF NOT EXISTS objects_kind_updated ON objects(kind,updated_at DESC);
CREATE INDEX IF NOT EXISTS objects_kind_name_id ON objects(kind,name,id);
CREATE TABLE IF NOT EXISTS events(id bigserial PRIMARY KEY,time timestamptz NOT NULL DEFAULT now(),kind text NOT NULL,object_id text NOT NULL,action text NOT NULL,detail jsonb NOT NULL DEFAULT '{}');
CREATE TABLE IF NOT EXISTS sessions(token_hash text PRIMARY KEY,csrf text NOT NULL,expires_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS worker_status(id text PRIMARY KEY,seen_at timestamptz NOT NULL DEFAULT now(),generation bigint NOT NULL DEFAULT 0,status jsonb NOT NULL DEFAULT '{}');
CREATE SEQUENCE IF NOT EXISTS config_generation;`)
	if e != nil {
		db.Close()
		return nil, e
	}
	_, e = db.Exec(ctx, `ALTER TABLE objects ADD COLUMN IF NOT EXISTS queue_state text NOT NULL DEFAULT ''; CREATE INDEX IF NOT EXISTS objects_pending ON objects(queue_state,updated_at) WHERE kind='operations';
CREATE INDEX IF NOT EXISTS objects_operations_recent ON objects ((CASE WHEN queue_state IN ('queued','running') THEN 0 ELSE 1 END),updated_at DESC,id) WHERE kind='operations';`)
	if e != nil {
		db.Close()
		return nil, e
	}
	// The epoch changes in the same transaction as every profile mutation, including
	// bulk restore and direct SQL. Readers can reuse immutable decoded catalogues
	// without a TTL that might delay credential or permission changes.
	_, e = db.Exec(ctx, `CREATE TABLE IF NOT EXISTS catalogue_epoch(id boolean PRIMARY KEY DEFAULT true CHECK(id),version bigint NOT NULL);
INSERT INTO catalogue_epoch(id,version) VALUES(true,1) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION bump_catalogue_epoch() RETURNS trigger LANGUAGE plpgsql AS $body$
BEGIN
 IF (TG_OP='DELETE' AND OLD.kind='profiles') OR (TG_OP<>'DELETE' AND NEW.kind='profiles') THEN
  UPDATE catalogue_epoch SET version=version+1 WHERE id=true;
 END IF;
 RETURN NULL;
END $body$;
CREATE OR REPLACE TRIGGER objects_catalogue_epoch AFTER INSERT OR UPDATE OR DELETE ON objects FOR EACH ROW EXECUTE FUNCTION bump_catalogue_epoch();`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func keyFile(path string) ([]byte, error) {
	if b, e := os.ReadFile(path); e == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("invalid master key")
		}
		return b, nil
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(e) {
		return os.ReadFile(path)
	}
	if e != nil {
		return nil, e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	f.Close()
	return b, e
}
func NewID() string {
	var b [12]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func (s *Store) Seal(kind, id string, plain []byte) ([]byte, error) {
	n := make([]byte, s.AEAD.NonceSize())
	if _, e := rand.Read(n); e != nil {
		return nil, e
	}
	return s.AEAD.Seal(n, n, plain, []byte(kind+"/"+id)), nil
}
func (s *Store) Open(kind, id string, sealed []byte) ([]byte, error) {
	n := s.AEAD.NonceSize()
	if len(sealed) < n {
		return nil, fmt.Errorf("corrupt encrypted object")
	}
	return s.AEAD.Open(nil, sealed[:n], sealed[n:], []byte(kind+"/"+id))
}
func (s *Store) Get(ctx context.Context, kind, id string) (Object, error) {
	var o Object
	var data []byte
	o.Kind = kind
	o.ID = id
	e := s.DB.QueryRow(ctx, `SELECT name,version,updated_at,data FROM objects WHERE kind=$1 AND id=$2`, kind, id).Scan(&o.Name, &o.Version, &o.UpdatedAt, &data)
	if errors.Is(e, pgx.ErrNoRows) {
		return o, ErrNotFound
	}
	if e != nil {
		return o, e
	}
	o.Data, e = s.Open(kind, id, data)
	return o, e
}
func (s *Store) List(ctx context.Context, kind string, limit, offset int) ([]Object, int, error) {
	if limit < 1 || limit > 20000 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if e := s.DB.QueryRow(ctx, `SELECT count(*) FROM objects WHERE kind=$1`, kind).Scan(&total); e != nil {
		return nil, 0, e
	}
	query := `SELECT id,name,version,updated_at,data FROM objects WHERE kind=$1 ORDER BY name,id LIMIT $2 OFFSET $3`
	args := []any{kind, limit, offset}
	if kind == "operations" {
		// Page after prioritising live jobs and recent history. Sorting only the
		// returned page leaves current operations hidden behind old apply jobs.
		query = `SELECT id,name,version,updated_at,data FROM objects WHERE kind='operations' ORDER BY CASE WHEN queue_state IN ('queued','running') THEN 0 ELSE 1 END,updated_at DESC,id LIMIT $1 OFFSET $2`
		args = []any{limit, offset}
	}
	rows, e := s.DB.Query(ctx, query, args...)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	out := make([]Object, 0, max(0, min(limit, total-offset)))
	for rows.Next() {
		o := Object{Kind: kind}
		var data []byte
		if e = rows.Scan(&o.ID, &o.Name, &o.Version, &o.UpdatedAt, &data); e != nil {
			return nil, 0, e
		}
		o.Data, e = s.Open(kind, o.ID, data)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, o)
	}
	return out, total, rows.Err()
}
func (s *Store) Put(ctx context.Context, o Object, expected int64) (Object, error) {
	if o.ID == "" {
		o.ID = NewID()
	}
	if limit := KindBudget(o.Kind); expected == 0 && limit > 0 {
		release, e := s.AdvisoryLock(ctx, 723501992)
		if e != nil {
			return o, e
		}
		defer release()
		var count int
		if e = s.DB.QueryRow(ctx, `SELECT count(*) FROM objects WHERE kind=$1 AND id<>$2`, o.Kind, o.ID).Scan(&count); e != nil {
			return o, e
		}
		if count >= limit {
			return o, ErrLimit
		}
	}
	if o.Kind == "" || o.Name == "" || !json.Valid(o.Data) {
		return o, fmt.Errorf("kind, name and JSON data required")
	}
	sealed, e := s.Seal(o.Kind, o.ID, o.Data)
	if e != nil {
		return o, e
	}
	phase := ""
	if o.Kind == "operations" {
		var v struct {
			Status string `json:"status"`
		}
		json.Unmarshal(o.Data, &v)
		phase = v.Status
	}
	if expected == 0 {
		e = s.DB.QueryRow(ctx, `INSERT INTO objects(kind,id,name,version,data,queue_state) VALUES($1,$2,$3,1,$4,$5) ON CONFLICT DO NOTHING RETURNING version,updated_at`, o.Kind, o.ID, o.Name, sealed, phase).Scan(&o.Version, &o.UpdatedAt)
	} else {
		e = s.DB.QueryRow(ctx, `UPDATE objects SET name=$3,data=$4,version=version+1,updated_at=now(),queue_state=$6 WHERE kind=$1 AND id=$2 AND version=$5 RETURNING version,updated_at`, o.Kind, o.ID, o.Name, sealed, expected, phase).Scan(&o.Version, &o.UpdatedAt)
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return o, ErrConflict
	}
	return o, e
}
func KindBudget(kind string) int {
	return map[string]int{"sources": 50, "profiles": 20000, "aggregates": 100, "pools": 100, "policies": 8, "listeners": 32, "settings": 1, "client-exports": 32}[kind]
}
func (s *Store) AdvisoryLock(ctx context.Context, key int64) (func(), error) {
	conn, e := s.DB.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	if _, e = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); e != nil {
		conn.Release()
		return nil, e
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, e := conn.Exec(cleanup, `SELECT pg_advisory_unlock($1)`, key); e != nil {
			conn.Conn().Close(cleanup)
		}
		conn.Release()
	}, nil
}
func (s *Store) Pending(ctx context.Context) ([]Object, error) {
	rows, e := s.DB.Query(ctx, `SELECT id,name,version,updated_at,data FROM objects WHERE kind='operations' AND (queue_state='queued' OR (queue_state='running' AND updated_at<now()-interval '5 minutes')) ORDER BY updated_at LIMIT 32`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Object{}
	for rows.Next() {
		o := Object{Kind: "operations"}
		var b []byte
		if e = rows.Scan(&o.ID, &o.Name, &o.Version, &o.UpdatedAt, &b); e != nil {
			return nil, e
		}
		o.Data, e = s.Open(o.Kind, o.ID, b)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func (s *Store) Delete(ctx context.Context, kind, id string, version int64) error {
	tag, e := s.DB.Exec(ctx, `DELETE FROM objects WHERE kind=$1 AND id=$2 AND version=$3`, kind, id, version)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) Event(ctx context.Context, kind, id, action string, detail any) error {
	b, e := json.Marshal(detail)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec(ctx, `INSERT INTO events(kind,object_id,action,detail) VALUES($1,$2,$3,$4)`, kind, id, action, b)
	return e
}
func (s *Store) Events(ctx context.Context, after int64) ([]Event, error) {
	query := `SELECT id,time,kind,object_id,action,detail FROM events WHERE id>$1 ORDER BY id ASC LIMIT 200`
	if after == 0 {
		query = `SELECT id,time,kind,object_id,action,detail FROM (SELECT * FROM events ORDER BY id DESC LIMIT 200) recent WHERE id>$1 ORDER BY id ASC`
	}
	rows, e := s.DB.Query(ctx, query, after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var v Event
		if e = rows.Scan(&v.ID, &v.Time, &v.Kind, &v.ObjectID, &v.Action, &v.Detail); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type Export struct {
	Schema  int       `json:"schema"`
	Created time.Time `json:"created_at"`
	Objects []Object  `json:"objects"`
}

func (s *Store) Export(ctx context.Context) (Export, error) {
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return Export{}, e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `SELECT kind,id,name,version,updated_at,data FROM objects WHERE kind IN ('sources','aggregates','pools','profiles','policies','listeners','settings','core-artifacts') ORDER BY kind,id`)
	if e != nil {
		return Export{}, e
	}
	defer rows.Close()
	out := Export{Schema: 1, Created: time.Now().UTC(), Objects: []Object{}}
	for rows.Next() {
		var o Object
		var data []byte
		if e = rows.Scan(&o.Kind, &o.ID, &o.Name, &o.Version, &o.UpdatedAt, &data); e != nil {
			return out, e
		}
		o.Data, e = s.Open(o.Kind, o.ID, data)
		if e != nil {
			return out, e
		}
		out.Objects = append(out.Objects, o)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	rows.Close()
	return out, tx.Commit(ctx)
}
func (s *Store) Import(ctx context.Context, in Export) error {
	if in.Schema != 1 || len(in.Objects) > 50000 {
		return fmt.Errorf("unsupported backup schema or object limit")
	}
	prepared := make([][]byte, len(in.Objects))
	hold, err := s.Seal("internal", "restore-pending", []byte(`{"apply_required":true}`))
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, o := range in.Objects {
		key := o.Kind + "/" + o.ID
		if o.Kind == "" || o.ID == "" || o.Name == "" || strings.Contains(key, "\x00") || seen[key] || !json.Valid(o.Data) {
			return fmt.Errorf("invalid backup object")
		}
		seen[key] = true
		b, e := s.Seal(o.Kind, o.ID, o.Data)
		if e != nil {
			return e
		}
		prepared[i] = b
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(723501992)`); e != nil {
		return e
	}
	// Serialize the import hold with live-fence publication. Acquire this after
	// the catalogue-budget lock and before touching rows, matching undo paths.
	// A refresh that began before import must not publish the restored draft.
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(723501993)`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `LOCK TABLE objects IN EXCLUSIVE MODE; DELETE FROM objects WHERE kind IN ('sources','aggregates','pools','profiles','policies','listeners','settings','core-artifacts')`); e != nil {
		return e
	}
	for i, o := range in.Objects {
		if _, e = tx.Exec(ctx, `INSERT INTO objects(kind,id,name,version,data) VALUES($1,$2,$3,$4,$5)`, o.Kind, o.ID, o.Name, max(o.Version, 1)+1000000, prepared[i]); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO objects(kind,id,name,version,data) VALUES('internal','restore-pending','Restored draft',1,$1) ON CONFLICT(kind,id) DO UPDATE SET version=objects.version+1,data=excluded.data,updated_at=now()`, hold); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// AtomicFile is used for last-known-good data and cryptographic state.
func AtomicFile(path string, b []byte, mode os.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".state-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(mode); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	f.Close()
	if e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func ReadBounded(r io.Reader, max int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, max+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("size limit exceeded")
	}
	return b, nil
}
