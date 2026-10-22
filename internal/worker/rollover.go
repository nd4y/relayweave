package worker

import (
	"strings"
	"time"
)

type pathRollover struct {
	quality                          *rolloverQuality
	serial, durableSerial            uint64
	old, next                        *Route
	state, reason                    string
	started, changed                 time.Time
	step                             int
	sequence                         uint64
	success, failed                  int
	lastAdmission, windowAt, retryAt time.Time
	windowBytes                      int64
	attempts                         int
}

var rolloverShares = [...]int{5, 20, 50, 100}

func (r *Runtime) installRolloversLocked(previous map[string]*engineEntry, now time.Time) {
	if r.rollovers == nil {
		r.rollovers = map[string]*pathRollover{}
	}
	for id, next := range r.current {
		old := previous[id]
		if old == nil || old == next {
			continue
		}
		if plan := r.rollovers[id]; plan != nil && plan.next == next.route {
			continue
		}
		baseline := old.route
		if prior := r.rollovers[id]; prior != nil && prior.state != "active_draining" && prior.state != "completed" {
			// A superseded canary remains a drain-only engine through its refs;
			// issuing a few tickets never makes it a last-known-good baseline.
			baseline = prior.old
		}
		r.rollovers[id] = &pathRollover{old: baseline, next: next.route, state: "warming", reason: "collecting_protocol_evidence", started: now, changed: now, serial: 1}
		r.rolloverDirty.Store(true)
	}
	for id, plan := range r.rollovers {
		if r.current[id] == nil || r.current[id].route != plan.next {
			delete(r.rollovers, id)
		}
	}
}

func (r *Runtime) advanceRolloversLocked(now time.Time) {
	for _, p := range r.rollovers {
		if p.state == "completed" {
			continue
		}
		previousState, previousStep := p.state, p.step
		if r.establishedOnlyLocked(p.next.Config) {
			p.state = "active_draining"
			p.reason = "source_profile_removed"
		}
		defer func(p *pathRollover, state string, step int) {
			if state != p.state || step != p.step {
				p.serial++
				r.rolloverDirty.Store(true)
			}
		}(p, previousState, previousStep)
		if p.state == "active_draining" && p.old.Active.Load() == 0 && p.old.References.Load() == 0 && (!p.next.Config.UDP || udpProven(p.next)) {
			present := false
			for _, e := range r.entries {
				present = present || e.route == p.old
			}
			if !present {
				p.state = "completed"
				p.reason = "old_sessions_finished"
				continue
			}
		}
		if p.state == "rolled_back" || p.state == "paused" {
			if p.retryAt.IsZero() || now.Before(p.retryAt) {
				continue
			}
			p.state = "warming"
			p.reason = "automatic_retry"
			p.started = now
			p.changed = now
			p.failed = 0
			p.success = 0
			p.step = 0
			p.attempts++
			p.quality = nil
		}
		if locallyFailedForAdmission(p.next) || p.failed >= 5 {
			p.state = "rolled_back"
			p.reason = "candidate_protocol_failures"
			p.changed = now
			p.retryAt = now.Add(5 * time.Minute)
			if p.attempts >= 3 {
				p.retryAt = now.Add(time.Hour)
			}
			continue
		}
		if !p.next.Instance.Ready.Load() {
			continue
		}
		if (p.state == "warming" || p.state == "canary") && now.Sub(p.started) > 30*time.Minute {
			p.state = "paused"
			p.reason = "insufficient_samples"
			p.retryAt = now.Add(5 * time.Minute)
			continue
		}
		qualityReady := true
		if p.next.Config.CheckServer && (p.state == "warming" || p.state == "canary") {
			verdict := r.qualityVerdictLocked(p, now)
			qualityReady = verdict.Allow
			p.reason = verdict.Reason
			if verdict.Pause || (p.quality != nil && p.quality.Blocked) {
				p.state = "paused"
				p.retryAt = now.Add(5 * time.Minute)
				p.changed = now
				continue
			}
		}
		if p.state == "warming" {
			if p.next.Instance.ProbeSamples.Load() < 3 || now.Sub(p.started) < 15*time.Second || !qualityReady {
				continue
			}
			p.state = "canary"
			p.reason = "protocol_checks_passed"
			p.changed = now
		}
		if p.state == "canary" && qualityReady && now.Sub(p.changed) >= 30*time.Second && p.success >= 20 && p.failed == 0 {
			if p.next.Samples.Load() >= 20 && p.old.Samples.Load() >= 20 {
				baseline := p.old.RTTUS.Load()
				if p.next.RTTUS.Load() > baseline+max(baseline/5, 20000) {
					p.state = "paused"
					p.reason = "latency_regression"
					p.retryAt = now.Add(5 * time.Minute)
					continue
				}
			}
			p.step++
			p.success = 0
			p.changed = now
			if p.step >= len(rolloverShares)-1 {
				p.step = len(rolloverShares) - 1
				p.state = "active_draining"
				p.reason = "canary_passed"
			}
		}
		if p.state == "canary" && now.Sub(p.started) > 30*time.Minute {
			p.state = "paused"
			p.reason = "insufficient_samples"
			p.retryAt = now.Add(5 * time.Minute)
		}
	}
}

func (r *Runtime) chooseRevisionLocked(v *Route, now time.Time) *Route {
	p := r.rollovers[v.Config.ID]
	if p == nil || (v != p.next && v != p.old) {
		return v
	}
	if p.durableSerial < p.serial {
		return p.old
	}
	if p.state == "warming" || p.state == "paused" || p.state == "rolled_back" {
		return p.old
	}
	if p.state == "active_draining" {
		return p.next
	}
	if p.state == "canary" {
		if p.next.Config.CheckServer && (p.quality == nil || p.quality.Blocked || !r.qualityVerdictLocked(p, now).Allow) {
			return p.old
		}
		p.sequence++
		if int(((p.sequence-1)*37)%100) >= rolloverShares[p.step] {
			return p.old
		}
		if p.step == 0 && !r.firstCanaryAdmissionAllowedLocked(p, now) {
			return p.old
		}
		p.lastAdmission = now
		return p.next
	}
	return v
}

func (r *Runtime) rolloverStatusLocked() []map[string]any {
	out := []map[string]any{}
	for id, p := range r.rollovers {
		share := 0
		if p.state == "canary" {
			share = rolloverShares[p.step]
		} else if p.state == "active_draining" {
			share = 100
		}
		out = append(out, map[string]any{"source_id": strings.SplitN(p.next.Config.CapacityGroup, "/", 2)[0], "route": id, "egress": p.next.Config.EgressID, "state": p.state, "reason": p.reason, "new_admission_percent": share, "successful_sessions": p.success, "failed_sessions": p.failed, "old_active": p.old.Active.Load(), "udp_ready": udpProven(p.next), "started_at": p.started, "stage_started_at": p.changed, "next_retry_at": p.retryAt})
		if p.next.Config.CheckServer {
			v := r.qualityVerdictLocked(p, time.Now())
			out[len(out)-1]["quality"] = map[string]any{"mode": v.Mode, "reason": v.Reason, "confidence": v.Confidence, "samples": v.Samples, "paired_samples": v.PairedSamples, "bulk_state": v.BulkState, "bulk_ratio": v.BulkRatio}
		}
	}
	return out
}
