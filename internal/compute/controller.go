package compute

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

type Controller struct {
	Repo     *Repository
	Key      ed25519.PrivateKey
	KeyID    string
	Epoch    atomic.Int64
	mu       sync.Mutex
	rates    map[string]requestRate
	wake     chan struct{}
	Renew    func(string, []byte) ([]byte, error)
	Maintain func() error
	polling  map[string]bool
	queue    chan Snapshot
	latest   map[string][]Recommendation
}
type requestRate struct {
	at     time.Time
	tokens float64
}

func (c *Controller) Run(ctx context.Context) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		call, cancel := context.WithTimeout(ctx, time.Second)
		epoch, e := c.Repo.Acquire(call)
		cancel()
		if e != nil {
			c.Epoch.Store(0)
		} else {
			c.Epoch.Store(epoch)
			call, done := context.WithTimeout(ctx, time.Second)
			_ = c.publishPending(call)
			_ = c.refreshLatest(call)
			done()
		}
		if c.Maintain != nil {
			_ = c.Maintain()
		}
		select {
		case <-ctx.Done():
			c.Epoch.Store(0)
			return
		case <-tick.C:
		case snapshot := <-c.queueChannel():
			call, done := context.WithTimeout(ctx, time.Second)
			_ = c.Offer(call, snapshot)
			done()
		}
	}
}

func (c *Controller) queueChannel() chan Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queue == nil {
		c.queue = make(chan Snapshot, 8)
	}
	return c.queue
}
func (c *Controller) Submit(s Snapshot) bool {
	select {
	case c.queueChannel() <- s:
		return true
	default:
		return false
	}
}
func (c *Controller) CachedLatest(executor string) []Recommendation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Recommendation(nil), c.latest[executor]...)
}
func (c *Controller) refreshLatest(ctx context.Context) error {
	rows, e := c.Repo.DB.Query(ctx, `SELECT payload FROM compute_publication WHERE installation=$1 AND epoch=$2 AND created_at>clock_timestamp()-interval '30 seconds' ORDER BY sequence DESC LIMIT 32`, c.Repo.Installation, c.Epoch.Load())
	if e != nil {
		return e
	}
	defer rows.Close()
	latest := map[string][]Recommendation{}
	for rows.Next() {
		var raw []byte
		var rec Recommendation
		if e = rows.Scan(&raw); e != nil {
			return e
		}
		if e = StrictDecode(raw, &rec); e != nil {
			return e
		}
		latest[rec.Snapshot.Executor] = append(latest[rec.Snapshot.Executor], rec)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	c.mu.Lock()
	c.latest = latest
	c.mu.Unlock()
	return nil
}

// Completed results form a durable outbox. A crash after receipt is recovered
// on the next owner iteration. Publish checks the current owner before signing.
func (c *Controller) publishPending(ctx context.Context) error {
	rows, e := c.Repo.DB.Query(ctx, `SELECT a.id,a.payload,a.result FROM compute_attempt a WHERE installation=$1 AND epoch=$2 AND state='completed' AND NOT publication_checked AND result->>'status'='ok' AND (result->'candidate'->>'id')::bigint>0 AND expires_at>clock_timestamp()-interval '15 seconds' ORDER BY expires_at LIMIT 2`, c.Repo.Installation, c.Epoch.Load())
	if e != nil {
		return e
	}
	type item struct {
		id     string
		task   Task
		result Result
	}
	pending := []item{}
	for rows.Next() {
		var a, b []byte
		var v item
		if e = rows.Scan(&v.id, &a, &b); e != nil {
			rows.Close()
			return e
		}
		if StrictDecode(a, &v.task) != nil || StrictDecode(b, &v.result) != nil {
			rows.Close()
			return ErrInvalid
		}
		pending = append(pending, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, v := range pending {
		if _, e = c.Repo.Publish(ctx, c.Epoch.Load(), v.task.Snapshot, v.result.Candidate, c.KeyID, c.Key); e != nil {
			return e
		}
		tx, err := c.Repo.DB.Begin(ctx)
		if err != nil {
			return err
		}
		if err = c.Repo.lock(ctx, tx, c.Epoch.Load()); err == nil {
			_, err = tx.Exec(ctx, `UPDATE compute_attempt SET publication_checked=true WHERE installation=$1 AND id=$2`, c.Repo.Installation, v.id)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) Publish(ctx context.Context, epoch int64, s Snapshot, candidate Candidate, keyID string, key ed25519.PrivateKey) (Recommendation, error) {
	var recommendation Recommendation
	if e := Verify(ctx, s, candidate); e != nil {
		return recommendation, e
	}
	tx, e := r.DB.Begin(ctx)
	if e != nil {
		return recommendation, e
	}
	defer tx.Rollback(ctx)
	if e = r.lock(ctx, tx, epoch); e != nil {
		return recommendation, e
	}
	h, _ := Hash(s)
	var previous []byte
	e = tx.QueryRow(ctx, `SELECT payload FROM compute_publication WHERE installation=$1 AND snapshot_digest=$2 FOR UPDATE`, r.Installation, h).Scan(&previous)
	if e == nil {
		var old Recommendation
		if e = StrictDecode(previous, &old); e != nil {
			return recommendation, e
		}
		oldMetrics, e1 := Evaluate(ctx, s, old.Candidate)
		newMetrics, e2 := Evaluate(ctx, s, candidate)
		if old.OwnerEpoch == epoch && e1 == nil && e2 == nil && newMetrics.Cost >= oldMetrics.Cost-1e-9 {
			return old, nil
		}
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return recommendation, e
	}
	var seq int64
	if e = tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('compute_publication','sequence'))`).Scan(&seq); e != nil {
		return recommendation, e
	}
	recommendation = Recommendation{Version: 1, KeyID: keyID, OwnerEpoch: epoch, Sequence: seq, Snapshot: s, SnapshotDigest: h, Candidate: candidate, BlendPPM: 100000}
	recommendation, e = Sign(recommendation, key)
	if e != nil {
		return recommendation, e
	}
	raw, e := Canonical(recommendation)
	if e != nil {
		return recommendation, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO compute_publication(installation,snapshot_digest,sequence,epoch,payload)VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(installation,snapshot_digest)DO UPDATE SET sequence=excluded.sequence,epoch=excluded.epoch,payload=excluded.payload,created_at=clock_timestamp()`, r.Installation, h, seq, epoch, raw)
	if e != nil {
		return recommendation, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO compute_audit(installation,actor,action,subject,detail)VALUES($1,$2,'publication',$3,jsonb_build_object('digest',$3::text,'sequence',$4::bigint,'key_id',$5::text))`, r.Installation, r.Owner, h, seq, keyID)
	if e != nil {
		return recommendation, e
	}
	e = tx.Commit(ctx)
	return recommendation, e
}

func (c *Controller) Latest(ctx context.Context, executor string) ([]Recommendation, error) {
	rows, e := c.Repo.DB.Query(ctx, `SELECT payload FROM compute_publication WHERE installation=$1 AND epoch=$2 AND payload->'snapshot'->>'executor'=$3 AND created_at>clock_timestamp()-interval '30 seconds' ORDER BY sequence DESC LIMIT 32`, c.Repo.Installation, c.Epoch.Load(), executor)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Recommendation{}
	for rows.Next() {
		var raw []byte
		var r Recommendation
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = StrictDecode(raw, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (c *Controller) Offer(ctx context.Context, s Snapshot) error {
	e := c.Repo.Offer(ctx, c.Epoch.Load(), s)
	if e == nil {
		c.mu.Lock()
		if c.wake != nil {
			close(c.wake)
		}
		c.wake = make(chan struct{})
		c.mu.Unlock()
	}
	return e
}
func (c *Controller) watch() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wake == nil {
		c.wake = make(chan struct{})
	}
	return c.wake
}

func (c *Controller) rate(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rates == nil {
		c.rates = map[string]requestRate{}
	}
	if len(c.rates) >= 64 {
		for key, entry := range c.rates {
			if time.Since(entry.at) > 10*time.Minute {
				delete(c.rates, key)
			}
		}
		if _, ok := c.rates[id]; !ok {
			if len(c.rates) >= 64 {
				return false
			}
		}
	}
	r, exists := c.rates[id]
	now := time.Now()
	if !exists {
		r = requestRate{at: now, tokens: 4}
	}
	r.tokens = math.Min(4, r.tokens+now.Sub(r.at).Seconds()*2)
	r.at = now
	allowed := r.tokens >= 1
	if allowed {
		r.tokens--
	}
	c.rates[id] = r
	return allowed
}

type sessionRequest struct {
	Version int    `json:"version"`
	Session string `json:"session"`
	Request string `json:"request"`
}
type registerResponse struct {
	Version int    `json:"version"`
	Session string `json:"session"`
}

// An authenticated peer cannot avoid quarantine by disconnecting after sending
// an invalid result. Accounting has its own bounded lifetime, and must commit
// before the protocol rejection is returned.
func (c *Controller) recordInvalidResult(r *http.Request, id string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
	defer cancel()
	return c.Repo.InvalidResult(ctx, id)
}

func (c *Controller) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || len(r.RequestURI) > 64 {
		http.Error(w, "compute_invalid_path", 400)
		return
	}
	if r.Method != http.MethodPost || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "compute_identity_required", http.StatusForbidden)
		return
	}
	peer := r.TLS.PeerCertificates[0]
	// TLS verification belongs to the handshake; recheck validity for requests
	// on reused connections as well as the current database revocation state.
	now := time.Now()
	for _, cert := range r.TLS.VerifiedChains[0] {
		if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
			http.Error(w, "compute_identity_expired", 403)
			return
		}
	}
	id := peer.Subject.CommonName
	if !opaque(id) || peer.NotAfter.Sub(peer.NotBefore) > 7*24*time.Hour+10*time.Minute || !c.rate(id) {
		http.Error(w, "compute_rate_or_identity", http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	var approved bool
	public, e := x509.MarshalPKIXPublicKey(peer.PublicKey)
	if e != nil {
		http.Error(w, "compute_identity", 403)
		return
	}
	if e := c.Repo.DB.QueryRow(ctx, `SELECT approved AND NOT revoked AND key_hash=$3 FROM compute_worker WHERE installation=$1 AND id=$2`, c.Repo.Installation, id, tokenHash(string(public))).Scan(&approved); e != nil || !approved {
		http.Error(w, "compute_not_approved", http.StatusForbidden)
		return
	}
	if c.Epoch.Load() == 0 {
		http.Error(w, "compute_owner_unavailable", 503)
		return
	}
	isResult := r.URL.Path == "/compute/v1/result" || r.URL.Path == "/compute/v1/cancelled"
	rejectBody := func(reason string, code int) {
		if isResult && c.recordInvalidResult(r, id) != nil {
			http.Error(w, "compute_authority_unavailable", 503)
			return
		}
		http.Error(w, reason, code)
	}
	if r.Header.Get("Content-Encoding") != "" {
		rejectBody("compute_compression_disabled", 400)
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, (32<<10)+1))
	if e != nil {
		// A failed/partial upload is a transport failure, not an invalid result.
		http.Error(w, "compute_body_limit", 413)
		return
	}
	if len(raw) > 32<<10 {
		rejectBody("compute_body_limit", 413)
		return
	}
	var response any
	code := 200
	switch r.URL.Path {
	case "/compute/v1/renew":
		var request struct {
			CSR []byte `json:"csr"`
		}
		if StrictDecode(raw, &request) != nil || len(request.CSR) > 2048 || c.Renew == nil {
			e = ErrInvalid
			break
		}
		csr, err := x509.ParseCertificateRequest(request.CSR)
		if err != nil || csr.CheckSignature() != nil {
			e = ErrInvalid
			break
		}
		spki, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
		if err != nil || tokenHash(string(spki)) != tokenHash(string(public)) {
			e = ErrInvalid
			break
		}
		var certificate []byte
		certificate, e = c.Renew(id, request.CSR)
		response = map[string][]byte{"certificate": certificate}
	case "/compute/v1/register":
		var request sessionRequest
		if StrictDecode(raw, &request) != nil || request.Version != 1 {
			e = ErrInvalid
			break
		}
		session, err := c.Repo.Register(ctx, id)
		e = err
		response = registerResponse{1, session}
	case "/compute/v1/heartbeat":
		var request sessionRequest
		if StrictDecode(raw, &request) != nil || request.Version != 1 {
			e = ErrInvalid
			break
		}
		e = c.Repo.Heartbeat(ctx, id, request.Session)
		response = map[string]int{"version": 1}
	case "/compute/v1/lease":
		c.mu.Lock()
		if c.polling == nil {
			c.polling = map[string]bool{}
		}
		busy := c.polling[id]
		if !busy {
			c.polling[id] = true
		}
		c.mu.Unlock()
		if busy {
			e = ErrConflict
			break
		}
		defer func() { c.mu.Lock(); delete(c.polling, id); c.mu.Unlock() }()
		var request sessionRequest
		if StrictDecode(raw, &request) != nil || request.Version != 1 {
			e = ErrInvalid
			break
		}
		wake := c.watch()
		lease := func() (Task, error) {
			call, done := context.WithTimeout(r.Context(), time.Second)
			defer done()
			return c.Repo.Lease(call, c.Epoch.Load(), id, request.Session, request.Request)
		}
		var task Task
		task, e = lease()
		if errors.Is(e, ErrNoJob) {
			timer := time.NewTimer(8 * time.Second)
			select {
			case <-wake:
				task, e = lease()
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				return
			}
			timer.Stop()
		}
		response = task
		if errors.Is(e, ErrNoJob) {
			call, done := context.WithTimeout(r.Context(), time.Second)
			e = c.Repo.RememberEmpty(call, c.Epoch.Load(), id, request.Request)
			done()
			if e == nil {
				code = 204
			}
		}
	case "/compute/v1/result", "/compute/v1/cancelled":
		var result Result
		if StrictDecode(raw, &result) != nil {
			e = ErrInvalid
			break
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			e = ErrInvalid
			break
		}
		var s Snapshot
		s, e = c.Repo.Accept(ctx, c.Epoch.Load(), id, token, result)
		if e == nil && result.Status == "ok" && result.Candidate.ID != 0 {
			_, e = c.Repo.Publish(ctx, c.Epoch.Load(), s, result.Candidate, c.KeyID, c.Key)
		}
		response = map[string]string{"receipt": result.Attempt}
	default:
		http.NotFound(w, r)
		return
	}
	if isResult && errors.Is(e, ErrInvalid) {
		if c.recordInvalidResult(r, id) != nil {
			http.Error(w, "compute_authority_unavailable", 503)
			return
		}
	}
	if e != nil {
		switch {
		case errors.Is(e, ErrInvalid):
			code = 400
		case errors.Is(e, ErrConflict):
			code = 409
		case errors.Is(e, ErrStale):
			code = 410
		case errors.Is(e, ErrDailyBudget):
			code = 429
			w.Header().Set("Retry-After", "86400")
		case errors.Is(e, ErrBudget):
			code = 429
			w.Header().Set("Retry-After", "10")
		default:
			code = 503
		}
		reason := "compute_unavailable"
		switch code {
		case 400:
			reason = "compute_invalid"
		case 409:
			reason = "compute_conflict"
		case 410:
			reason = "compute_stale"
		case 429:
			reason = "compute_budget"
		}
		http.Error(w, reason, code)
		return
	}
	var data []byte
	if code != 204 {
		data, e = Canonical(response)
		if e != nil {
			http.Error(w, "compute_encoding", 500)
			return
		}
	}
	if len(data) > 64<<10 {
		http.Error(w, "compute_response_limit", 500)
		return
	}
	// Bodies and received headers are counted. The extra allowance covers the
	// fixed request/status lines and bounded standard response headers.
	charge := int64(len(raw) + len(data) + len(r.Host) + len(r.RequestURI) + 512)
	for key, values := range r.Header {
		charge += int64(len(key) + 4)
		for _, value := range values {
			charge += int64(len(value) + 2)
		}
	}
	account, done := context.WithTimeout(r.Context(), time.Second)
	e = c.Repo.ChargeNetwork(account, c.Epoch.Load(), charge)
	done()
	if e != nil {
		if errors.Is(e, ErrDailyBudget) {
			w.Header().Set("Retry-After", "86400")
			http.Error(w, "compute_budget_exhausted", 429)
		} else if errors.Is(e, ErrBudget) {
			w.Header().Set("Retry-After", "10")
			http.Error(w, "compute_budget_exhausted", 429)
		} else {
			http.Error(w, "compute_authority_unavailable", 503)
		}
		return
	}
	if code == 204 {
		w.WriteHeader(code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(data)
}
