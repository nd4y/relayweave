package compute

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrStale = errors.New("stale_compute_lease")
var ErrBudget = errors.New("compute_budget_exhausted")
var ErrDailyBudget = errors.New("compute_daily_budget_exhausted")
var ErrConflict = errors.New("compute_conflict")
var ErrNoJob = errors.New("compute_no_job")

//go:embed schema_v1.sql
var schemaV1 string

type Repository struct {
	DB                  *pgxpool.Pool
	Installation, Owner string
	TokenKey            []byte
}

// Migrate is opt-in, additive, and confined to compute tables. It must never be
// called by a data-plane worker or when compute is disabled.
func (r *Repository) Migrate(ctx context.Context) error {
	if !opaque(r.Installation) || !opaque(r.Owner) || len(r.TokenKey) != 32 {
		return ErrInvalid
	}
	_, e := r.DB.Exec(ctx, schemaV1)
	if e != nil {
		return e
	}
	_, e = r.DB.Exec(ctx, `INSERT INTO compute_installation_state(installation)VALUES($1)ON CONFLICT DO NOTHING`, r.Installation)
	return e
}

func newID() string             { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func tokenHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (r *Repository) taskToken(t Task) string {
	mac := hmac.New(sha256.New, r.TokenKey)
	mac.Write([]byte("relayweave/compute/attempt/v1\x00" + r.Installation + "\x00" + t.ID + "\x00" + t.Attempt + "\x00" + t.Session))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Ownership and assignment/result/publication share the installation row lock.
// A paused former owner cannot commit a new assignment after takeover.
func (r *Repository) Acquire(ctx context.Context) (int64, error) {
	var epoch int64
	e := r.DB.QueryRow(ctx, `UPDATE compute_installation_state SET epoch=CASE WHEN owner=$2 AND lease_until>clock_timestamp() THEN epoch ELSE epoch+1 END,
 owner=$2,lease_until=clock_timestamp()+interval '10 seconds' WHERE installation=$1 AND
 (owner=$2 OR lease_until<clock_timestamp()) AND uncertain_until<=clock_timestamp() RETURNING epoch`, r.Installation, r.Owner).Scan(&epoch)
	if e != nil {
		return 0, ErrStale
	}
	return epoch, nil
}
func (r *Repository) lock(ctx context.Context, tx pgx.Tx, epoch int64) error {
	var current int64
	e := tx.QueryRow(ctx, `SELECT epoch FROM compute_installation_state WHERE installation=$1 AND owner=$2 AND epoch=$3
 AND lease_until>clock_timestamp() AND uncertain_until<=clock_timestamp() FOR UPDATE`, r.Installation, r.Owner, epoch).Scan(&current)
	if e != nil {
		return ErrStale
	}
	return nil
}

func (r *Repository) ApproveCertificate(ctx context.Context, id, keyHash string) error {
	if !opaque(id) || !digest(keyHash) {
		return ErrInvalid
	}
	e := r.auditedMutation(ctx, "worker_approved", id, `INSERT INTO compute_worker(installation,id,session,approved,key_hash)VALUES($1,$2,'',true,$3)
 ON CONFLICT(installation,id)DO UPDATE SET approved=true,revoked=false,key_hash=excluded.key_hash,session='',invalid_count=0,invalid_since='-infinity'`, r.Installation, id, keyHash)
	return e
}

func (r *Repository) InvalidResult(ctx context.Context, id string) error {
	e := r.auditedMutation(ctx, "invalid_result", id, `UPDATE compute_worker SET invalid_count=CASE WHEN invalid_since<clock_timestamp()-interval '10 minutes' THEN 1 ELSE invalid_count+1 END,invalid_since=CASE WHEN invalid_since<clock_timestamp()-interval '10 minutes' THEN clock_timestamp() ELSE invalid_since END,revoked=revoked OR (invalid_since>=clock_timestamp()-interval '10 minutes' AND invalid_count>=2) WHERE installation=$1 AND id=$2`, r.Installation, id)
	return e
}

func (r *Repository) Revoke(ctx context.Context, id string) error {
	e := r.auditedMutation(ctx, "worker_revoked", id, `UPDATE compute_worker SET revoked=true,approved=false WHERE installation=$1 AND id=$2`, r.Installation, id)
	return e
}
func (r *Repository) Register(ctx context.Context, id string) (string, error) {
	if !opaque(id) {
		return "", ErrInvalid
	}
	session := newID()
	tx, e := r.DB.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if e = r.currentOwner(ctx, tx); e != nil {
		return "", e
	}
	// Registration cannot replace the session of a still running job.
	var out string
	e = tx.QueryRow(ctx, `UPDATE compute_worker SET session=$3,seen_at=clock_timestamp(),registered_at=clock_timestamp() WHERE installation=$1 AND id=$2 AND approved AND NOT revoked AND registered_at<clock_timestamp()-interval '60 seconds'
 AND NOT EXISTS(SELECT 1 FROM compute_attempt WHERE installation=$1 AND worker=$2 AND state IN ('leased','running') AND expires_at+interval '62 seconds'>clock_timestamp()) RETURNING session`, r.Installation, id, session).Scan(&out)
	if e != nil {
		return "", ErrStale
	}
	return out, tx.Commit(ctx)
}
func (r *Repository) Heartbeat(ctx context.Context, id, session string) error {
	tx, e := r.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = r.currentOwner(ctx, tx); e != nil {
		return e
	}
	cmd, e := tx.Exec(ctx, `UPDATE compute_worker SET seen_at=clock_timestamp() WHERE installation=$1 AND id=$2 AND session=$3 AND approved AND NOT revoked`, r.Installation, id, session)
	if e != nil || cmd.RowsAffected() != 1 {
		return ErrStale
	}
	return tx.Commit(ctx)
}

func (r *Repository) currentOwner(ctx context.Context, tx pgx.Tx) error {
	var epoch int64
	if e := tx.QueryRow(ctx, `SELECT epoch FROM compute_installation_state WHERE installation=$1 AND owner=$2 AND lease_until>clock_timestamp() AND uncertain_until<=clock_timestamp() FOR UPDATE`, r.Installation, r.Owner).Scan(&epoch); e != nil {
		return ErrStale
	}
	return nil
}

func (r *Repository) Offer(ctx context.Context, epoch int64, s Snapshot) error {
	if s.Installation != r.Installation {
		return ErrInvalid
	}
	if e := s.Validate(); e != nil {
		return e
	}
	all, e := Generate(s)
	if e != nil || len(all) <= 1 {
		return nil
	}
	tx, e := r.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = r.lock(ctx, tx, epoch); e != nil {
		return e
	}
	h, _ := Hash(s)
	raw, _ := Canonical(s)
	var workers int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM compute_worker WHERE installation=$1 AND approved AND NOT revoked AND session<>'' AND seen_at>clock_timestamp()-interval '30 seconds'`, r.Installation).Scan(&workers); e != nil {
		return e
	}
	if workers == 0 {
		return nil
	}
	workers = min(workers, 2)
	for part := 0; part < workers; part++ {
		first := part * len(all) / workers
		end := (part + 1) * len(all) / workers
		_, e = tx.Exec(ctx, `INSERT INTO compute_task(installation,id,epoch,snapshot,snapshot_digest,candidate_first,candidate_count)VALUES($1,$2,$3,$4,$5,$6,$7)ON CONFLICT DO NOTHING`, r.Installation, newID(), epoch, raw, h, first, end-first)
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) reserve(ctx context.Context, tx pgx.Tx, cpu, network int64) error {
	var day string
	var bucket int64
	if e := tx.QueryRow(ctx, `SELECT to_char(clock_timestamp()AT TIME ZONE'UTC','YYYY-MM-DD'),floor(extract(epoch FROM clock_timestamp())/10)::bigint`).Scan(&day, &bucket); e != nil {
		return e
	}
	var safe bool
	if e := tx.QueryRow(ctx, `UPDATE compute_installation_state SET high_day=GREATEST(high_day,$2::date) WHERE installation=$1 RETURNING high_day=$2::date`, r.Installation, day).Scan(&safe); e != nil || !safe {
		return ErrBudget
	}
	var rolling int64
	if e := tx.QueryRow(ctx, `SELECT COALESCE(sum(reserved_cpu_ms),0)FROM compute_attempt WHERE installation=$1 AND reserved_at>clock_timestamp()-interval '10 seconds'`, r.Installation).Scan(&rolling); e != nil {
		return e
	}
	if rolling+cpu > 1000 {
		return ErrBudget
	}
	// Charge the neighboring UTC day conservatively when a job spans midnight.
	windows := []struct {
		key                string
		limitCPU, limitNet int64
	}{
		{"day/" + day, 10800000, 64 << 20}, {"epoch/" + strconvInt(bucket), 1000, 64 << 20},
	}
	var nextDay string
	if e := tx.QueryRow(ctx, `SELECT to_char((clock_timestamp()+interval '5 seconds')AT TIME ZONE'UTC','YYYY-MM-DD')`).Scan(&nextDay); e != nil {
		return e
	}
	if nextDay != day {
		windows = append(windows, struct {
			key                string
			limitCPU, limitNet int64
		}{"day/" + nextDay, 10800000, 64 << 20})
	}
	for _, w := range windows {
		_, e := tx.Exec(ctx, `INSERT INTO compute_budget_window(installation,window_id)VALUES($1,$2)ON CONFLICT DO NOTHING`, r.Installation, w.key)
		if e != nil {
			return e
		}
		var spent int64
		e = tx.QueryRow(ctx, `UPDATE compute_budget_window SET cpu_ms=cpu_ms+$3,network_bytes=network_bytes+$4 WHERE installation=$1 AND window_id=$2 AND cpu_ms+$3<=$5 AND network_bytes+$4<=$6 RETURNING cpu_ms`, r.Installation, w.key, cpu, network, w.limitCPU, w.limitNet).Scan(&spent)
		if e != nil {
			if len(w.key) >= 4 && w.key[:4] == "day/" {
				return ErrDailyBudget
			}
			return ErrBudget
		}
	}
	return nil
}

func (r *Repository) Lease(ctx context.Context, epoch int64, worker, session, request string) (Task, error) {
	var task Task
	if !opaque(request) {
		return task, ErrInvalid
	}
	tx, e := r.DB.Begin(ctx)
	if e != nil {
		return task, e
	}
	defer tx.Rollback(ctx)
	if e = r.lock(ctx, tx, epoch); e != nil {
		return task, e
	}
	var allowed bool
	e = tx.QueryRow(ctx, `SELECT approved AND NOT revoked AND session=$3 FROM compute_worker WHERE installation=$1 AND id=$2 FOR UPDATE`, r.Installation, worker, session).Scan(&allowed)
	if e != nil || !allowed {
		return task, ErrStale
	}
	var empty bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM compute_empty_request WHERE installation=$1 AND worker=$2 AND request_id=$3)`, r.Installation, worker, request).Scan(&empty); e != nil {
		return task, e
	}
	if empty {
		return task, ErrNoJob
	}
	var raw []byte
	var active bool
	e = tx.QueryRow(ctx, `SELECT payload,expires_at>clock_timestamp() AND state IN ('leased','running') AND epoch=$4 FROM compute_attempt WHERE installation=$1 AND worker=$2 AND request_id=$3`, r.Installation, worker, request, epoch).Scan(&raw, &active)
	if e == nil {
		if !active {
			return task, ErrStale
		}
		if e = StrictDecode(raw, &task); e != nil {
			return task, e
		}
		task.Token = r.taskToken(task)
		return task, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return task, e
	}
	_, e = tx.Exec(ctx, `UPDATE compute_attempt SET state='expired' WHERE installation=$1 AND state IN ('leased','running') AND expires_at+interval '62 seconds'<clock_timestamp()`, r.Installation)
	if e != nil {
		return task, e
	}
	var busy int
	e = tx.QueryRow(ctx, `SELECT count(*)FROM compute_attempt WHERE installation=$1 AND state IN ('leased','running')`, r.Installation).Scan(&busy)
	if e != nil {
		return task, e
	}
	if busy >= 2 {
		return task, ErrBudget
	}
	var id, hash string
	var first, count, remainingMS int
	e = tx.QueryRow(ctx, `SELECT id,snapshot,snapshot_digest,candidate_first,candidate_count,LEAST(5000,floor(extract(epoch FROM expires_at-clock_timestamp())*1000)::int) FROM compute_task t WHERE installation=$1 AND epoch=$2 AND expires_at>clock_timestamp()+interval '1 second'
 AND NOT EXISTS(SELECT 1 FROM compute_attempt a WHERE a.installation=t.installation AND a.task=t.id) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, r.Installation, epoch).Scan(&id, &raw, &hash, &first, &count, &remainingMS)
	if errors.Is(e, pgx.ErrNoRows) {
		return task, ErrNoJob
	}
	if e != nil {
		return task, e
	}
	if e = StrictDecode(raw, &task.Snapshot); e != nil {
		return task, e
	}
	task.Version, task.ID, task.Attempt, task.Session, task.OwnerEpoch = 1, id, newID(), session, epoch
	task.SnapshotDigest, task.First, task.Count, task.CPUReservationMS, task.WallBudgetMS = hash, uint32(first), uint32(count), 500, 5000
	task.WallBudgetMS = min(task.WallBudgetMS, remainingMS)
	if e = r.reserve(ctx, tx, 500, 16384); e != nil {
		return task, e
	}
	token := r.taskToken(task)
	raw, e = Canonical(task)
	if e != nil {
		return task, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO compute_attempt(installation,id,task,worker,session,request_id,epoch,token_hash,state,expires_at,payload,reserved_cpu_ms)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,'leased',clock_timestamp()+$10*interval '1 millisecond',$9,500)`, r.Installation, task.Attempt, id, worker, session, request, epoch, tokenHash(token), raw, task.WallBudgetMS)
	if e != nil {
		return task, e
	}
	e = tx.Commit(ctx)
	if e == nil {
		task.Token = token
	}
	return task, e
}

func (r *Repository) RememberEmpty(ctx context.Context, epoch int64, worker, request string) error {
	tx, e := r.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = r.lock(ctx, tx, epoch); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO compute_empty_request(installation,worker,request_id)VALUES($1,$2,$3)ON CONFLICT DO NOTHING`, r.Installation, worker, request)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (r *Repository) Accept(ctx context.Context, epoch int64, worker, token string, result Result) (Snapshot, error) {
	var s Snapshot
	if result.Version != 1 || result.OwnerEpoch != epoch || !opaque(result.Attempt) || !opaque(result.Task) || !opaque(result.Session) || !digest(result.SnapshotDigest) || result.CPUUsedMS < 0 || result.CPUUsedMS > 500 {
		return s, ErrInvalid
	}
	if result.Status != "ok" && result.Status != "no_improvement" && result.Status != "cancelled" && result.Status != "overload" && result.Status != "error" {
		return s, ErrInvalid
	}
	tx, e := r.DB.Begin(ctx)
	if e != nil {
		return s, e
	}
	defer tx.Rollback(ctx)
	if e = r.lock(ctx, tx, epoch); e != nil {
		return s, e
	}
	var raw []byte
	var storedHash, state string
	var valid, identity bool
	e = tx.QueryRow(ctx, `SELECT a.payload,a.token_hash,a.state,a.expires_at>clock_timestamp(),w.approved AND NOT w.revoked AND w.session=a.session
 FROM compute_attempt a JOIN compute_worker w ON w.installation=a.installation AND w.id=a.worker
 WHERE a.installation=$1 AND a.id=$2 AND a.task=$3 AND a.worker=$4 AND a.session=$5 AND a.epoch=$6 FOR UPDATE OF a`, r.Installation, result.Attempt, result.Task, worker, result.Session, epoch).Scan(&raw, &storedHash, &state, &valid, &identity)
	if e != nil || subtle.ConstantTimeCompare([]byte(storedHash), []byte(tokenHash(token))) != 1 || !identity {
		return s, ErrStale
	}
	var task Task
	if e = StrictDecode(raw, &task); e != nil {
		return s, e
	}
	s = task.Snapshot
	if task.SnapshotDigest != result.SnapshotDigest {
		return s, ErrInvalid
	}
	h, _ := Hash(result)
	if state == "completed" {
		var previous string
		e = tx.QueryRow(ctx, `SELECT result_digest FROM compute_attempt WHERE installation=$1 AND id=$2`, r.Installation, result.Attempt).Scan(&previous)
		if e == nil && previous == h {
			return s, nil
		}
		return s, ErrConflict
	}
	if state != "leased" && state != "running" || !valid {
		return s, ErrStale
	}
	if result.Status == "ok" {
		if result.Candidate.ID != 0 && (result.Candidate.ID < task.First || result.Candidate.ID >= task.First+task.Count) {
			return s, ErrInvalid
		}
		if result.Candidate.ID != 0 {
			if e = Verify(ctx, s, result.Candidate); e != nil {
				return s, e
			}
		}
	}
	data, _ := Canonical(result)
	_, e = tx.Exec(ctx, `UPDATE compute_attempt SET state='completed',result_digest=$3,result=$4 WHERE installation=$1 AND id=$2`, r.Installation, result.Attempt, h, data)
	if e != nil {
		return s, e
	}
	return s, tx.Commit(ctx)
}

func (r *Repository) Uncertain(ctx context.Context) error {
	e := r.auditedMutation(ctx, "authority_uncertain", r.Installation, `UPDATE compute_installation_state SET uncertain_until=date_trunc('day',clock_timestamp()AT TIME ZONE'UTC')AT TIME ZONE'UTC'+interval '2 days',lease_until='-infinity',epoch=epoch+1 WHERE installation=$1`, r.Installation)
	return e
}

func (r *Repository) Cleanup(ctx context.Context) error {
	for _, q := range []string{
		`DELETE FROM compute_audit WHERE installation=$1 AND created_at<clock_timestamp()-interval '7 days'`,
		`DELETE FROM compute_empty_request WHERE installation=$1 AND created_at<clock_timestamp()-interval '10 minutes'`, `DELETE FROM compute_task WHERE installation=$1 AND created_at<clock_timestamp()-interval '60 seconds'`, `DELETE FROM compute_attempt WHERE installation=$1 AND expires_at<clock_timestamp()-interval '24 hours'`, `DELETE FROM compute_publication WHERE installation=$1 AND created_at<clock_timestamp()-interval '60 seconds'`, `DELETE FROM compute_budget_window WHERE installation=$1 AND ((CASE WHEN window_id LIKE 'epoch/%' THEN substring(window_id from 7)::bigint<floor(extract(epoch FROM clock_timestamp())/10)-12 ELSE false END) OR (CASE WHEN window_id LIKE 'day/%' THEN substring(window_id from 5)::date<(clock_timestamp()AT TIME ZONE'UTC')::date-3 ELSE false END))`} {
		if _, e := r.DB.Exec(ctx, q, r.Installation); e != nil {
			return e
		}
	}
	return nil
}

// Keep integer window_id names independent from wall timezone formatting.
func (r *Repository) ChargeNetwork(ctx context.Context, epoch, bytes int64) error {
	if bytes < 0 || bytes > 96<<10 {
		return ErrInvalid
	}
	tx, e := r.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = r.lock(ctx, tx, epoch); e != nil {
		return e
	}
	if e = r.reserve(ctx, tx, 0, bytes); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func strconvInt(v int64) string { return strconv.FormatInt(v, 10) }

// Mutations and their audit entry commit atomically. No certificate,
// private key, token or route configuration is written to this audit table.
func (r *Repository) auditedMutation(ctx context.Context, action, subject, query string, args ...any) error {
	tx, e := r.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	changed, e := tx.Exec(ctx, query, args...)
	if e != nil {
		return e
	}
	if changed.RowsAffected() != 1 {
		return ErrStale
	}
	if _, e = tx.Exec(ctx, `INSERT INTO compute_audit(installation,actor,action,subject)VALUES($1,$2,$3,$4)`, r.Installation, r.Owner, action, subject); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
