package worker

import (
	"context"
	"proxyplatform/internal/config"
	"time"
)

// Only the authenticated encrypted journal may contain Old credentials.
type rolloverRecord struct {
	Route    string       `json:"route"`
	Old      config.Route `json:"old"`
	Next     string       `json:"next"`
	State    string       `json:"state"`
	Reason   string       `json:"reason"`
	Step     int          `json:"step"`
	Serial   uint64       `json:"serial"`
	Started  time.Time    `json:"started"`
	Changed  time.Time    `json:"changed"`
	RetryAt  time.Time    `json:"retry_at"`
	Attempts int          `json:"attempts"`
}

func (r *Runtime) rolloverRecordsLocked() []rolloverRecord {
	out := []rolloverRecord{}
	for id, p := range r.rollovers {
		out = append(out, rolloverRecord{Route: id, Old: p.old.Config, Next: baseFingerprint(p.next.Config), State: p.state, Reason: p.reason, Step: p.step, Serial: p.serial, Started: p.started, Changed: p.changed, RetryAt: p.retryAt, Attempts: p.attempts})
	}
	return out
}

func (r *Runtime) acknowledgeRollovers(records []rolloverRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rolloverDirty.Store(false)
	if len(records) != len(r.rollovers) {
		r.rolloverDirty.Store(true)
	}
	for _, record := range records {
		if p := r.rollovers[record.Route]; p != nil && p.serial == record.Serial {
			p.durableSerial = p.serial
		} else {
			r.rolloverDirty.Store(true)
		}
	}
}

func (r *Runtime) restoreRolloverEngines(ctx context.Context, c config.Config) map[string]*engineEntry {
	old := map[string]*engineEntry{}
	for _, record := range r.restoreRollovers {
		r.mu.RLock()
		current := r.current[record.Route]
		r.mu.RUnlock()
		if current == nil || record.State == "completed" {
			continue
		}
		if record.Step < 0 || record.Step >= len(rolloverShares) || !time.Now().Before(record.Old.ValidUntil) {
			continue
		}
		var next *config.Route
		for i := range c.Routes {
			if c.Routes[i].ID == record.Route {
				next = &c.Routes[i]
				break
			}
		}
		if next == nil || baseFingerprint(*next) != record.Next {
			continue
		}
		matched := false
		for _, permit := range next.Previous {
			if permit.Fingerprint == baseFingerprint(record.Old) && time.Now().Before(permit.ValidUntil) {
				matched = true
			}
		}
		if fingerprint(*next) == fingerprint(record.Old) {
			matched = true
		}
		if !matched {
			continue
		}
		if e, err := r.ensure(ctx, c, record.Old); err == nil {
			old[record.Route] = e
		}
	}
	return old
}

func (r *Runtime) installRecoveredRolloversLocked(old map[string]*engineEntry) {
	pending := []rolloverRecord{}
	for _, record := range r.restoreRollovers {
		e, next := old[record.Route], r.current[record.Route]
		if e == nil || next == nil {
			if record.State != "completed" && time.Now().Before(record.Old.ValidUntil) {
				pending = append(pending, record)
			}
			continue
		}
		e.staged = false
		r.rollovers[record.Route] = &pathRollover{old: e.route, next: next.route, state: "warming", reason: "revalidating_after_restart", started: time.Now(), changed: time.Now(), serial: record.Serial + 1, attempts: record.Attempts}
		if record.State == "paused" || record.State == "rolled_back" {
			p := r.rollovers[record.Route]
			p.state = record.State
			p.reason = record.Reason
			p.retryAt = record.RetryAt
		}
		r.rolloverDirty.Store(true)
	}
	r.restoreRollovers = pending
}
