package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"proxyplatform/internal/config"
	"sort"
	"time"
)

type rolloverQualitySpec struct {
	Signature                      [32]byte
	Policy                         string
	Targets                        []string
	Speed                          string
	SpeedConfigured, SpeedDisabled bool
	Budget                         int64
	Timeout                        time.Duration
}

func (r *Runtime) qualitySpecLocked(plan *pathRollover) (rolloverQualitySpec, bool) {
	if plan == nil || plan.old == nil || plan.next == nil || plan.old.Instance == nil || plan.next.Instance == nil {
		return rolloverQualitySpec{}, false
	}
	cfg := r.config
	if r.constraints != nil {
		cfg = *r.constraints
	}
	ids := []string{}
	policies := map[string]config.Policy{}
	for _, p := range cfg.Policies {
		for _, route := range p.Routes {
			if route == plan.next.Config.ID {
				ids = append(ids, p.ID)
				policies[p.ID] = p
				break
			}
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return rolloverQualitySpec{}, false
	}
	p := policies[ids[0]]
	targets := append([]string{p.ProbeURL}, p.ProbeFallbackURLs...)
	if p.ProbeURL == "" {
		targets = append([]string{cfg.ProbeURL}, cfg.ProbeFallbackURLs...)
	}
	unique := []string{}
	seen := map[string]bool{}
	for _, target := range targets {
		if target != "" && !seen[target] && len(unique) < 16 {
			unique = append(unique, target)
			seen[target] = true
		}
	}
	speed, configured := pairedBulkTarget(p.SpeedURL)
	timeout := time.Duration(p.ConnectTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	timeout = max(100*time.Millisecond, min(3*time.Second, timeout))
	spec := rolloverQualitySpec{Policy: p.ID, Targets: unique, Speed: speed, SpeedConfigured: configured, SpeedDisabled: p.DisableSpeedProbes, Budget: p.ProbeBudgetBytes, Timeout: timeout}
	identity := struct {
		Old, Next                                               config.Route
		OldCore, NextCore, OldSocket, NextSocket, Policy, Speed string
		Targets                                                 []string
		Disabled                                                bool
		Timeout                                                 time.Duration
		Version                                                 int
	}{Old: plan.old.Config, Next: plan.next.Config, OldCore: plan.old.Instance.SHA256, NextCore: plan.next.Instance.SHA256, Policy: p.ID, Targets: unique, Speed: speed, Disabled: p.DisableSpeedProbes, Timeout: timeout, Version: 1}
	// Expiry extensions do not change the path; current authorization is checked
	// independently before and after I/O. No stage restart on an unrelated source.
	identity.Old.ValidUntil, identity.Next.ValidUntil = time.Time{}, time.Time{}
	identity.Old.Previous, identity.Next.Previous = nil, nil
	for _, e := range r.entries {
		if e.route == plan.old {
			identity.OldSocket = e.dialIP
		}
		if e.route == plan.next {
			identity.NextSocket = e.dialIP
		}
	}
	encoded, _ := json.Marshal(identity)
	spec.Signature = sha256.Sum256(encoded)
	return spec, true
}

// UDP warmup/freshness has priority over optional quality measurements. A failed
// UDP capability with its bounded backoff does not starve healthy TCP quality.
func (r *Runtime) qualityUDPHasPriorityLocked(now time.Time) bool {
	seen := map[*Route]bool{}
	urgent := func(v *Route) bool {
		if v == nil || seen[v] {
			return false
		}
		seen[v] = true
		if !v.Config.CheckServer || !v.Config.UDP || !v.Instance.Ready.Load() || r.establishedOnlyLocked(v.Config) || !r.permitted("", v.Config) {
			return false
		}
		if v.UDPHealthy.Load() && v.UDPChecks.Load() >= 3 {
			return now.Unix()-v.UDPChecked.Load() >= 40
		}
		return v.UDPChecked.Load() == 0 || v.UDPHealthy.Load()
	}
	for _, e := range r.current {
		if urgent(e.route) {
			return true
		}
	}
	for _, p := range r.rollovers {
		if p.state != "completed" && (p.state != "active_draining" || !udpProvenAt(p.next, now)) && urgent(p.old) {
			return true
		}
	}
	return false
}

func (a *Agent) rolloverQualityLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if a.ctx.Err() != nil {
			return
		}
		a.probeRolloverQuality(a.ctx)
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *Agent) probeRolloverQuality(ctx context.Context) {
	if !a.probeMu.TryLock() {
		return
	}
	defer a.probeMu.Unlock()
	r := a.runtime
	if !r.applyMu.TryLock() {
		return
	}
	r.mu.Lock()
	now := time.Now()
	var plan *pathRollover
	var spec rolloverQualitySpec
	if !r.drained.Load() && now.Unix() <= r.lease.Load() && !r.qualityUDPHasPriorityLocked(now) {
		for _, p := range r.rollovers {
			if !p.next.Config.CheckServer || (p.state != "warming" && p.state != "canary") || !r.revisionReadyForAdmissionLocked(p.next, now) || r.establishedOnlyLocked(p.next.Config) {
				continue
			}
			candidate, ok := r.prepareQualityCacheLocked(p, now)
			if !ok || !r.permitted(candidate.Policy, p.next.Config) {
				continue
			}
			if p.quality == nil || p.quality.Signature != candidate.Signature {
				p.quality = &rolloverQuality{Signature: candidate.Signature}
				r.refreshQualityVerdictLocked(p, now)
			}
			q := p.quality
			gap := 10 * time.Second
			if !r.revisionReadyForAdmissionLocked(p.old, now) || !r.overlapAllowedLocked(candidate.Policy, p.old.Config) {
				gap = time.Second
			}
			if q.InFlight || (!q.LastAttempt.IsZero() && now.Sub(q.LastAttempt) < gap) {
				continue
			}
			if plan == nil || q.LastAttempt.Before(plan.quality.LastAttempt) || (q.LastAttempt.Equal(plan.quality.LastAttempt) && p.next.Config.ID < plan.next.Config.ID) {
				plan, spec = p, candidate
			}
		}
	}
	if plan == nil {
		r.mu.Unlock()
		r.applyMu.Unlock()
		return
	}
	q := plan.quality
	epoch := r.qualityEpoch
	verdict := q.evaluate(now, spec.SpeedConfigured, spec.SpeedDisabled)
	bulk := !spec.SpeedDisabled && spec.SpeedConfigured && verdict.Samples >= 3 && (q.LastBulk.IsZero() || now.Sub(q.LastBulk) >= 30*time.Second || verdict.Reason == "waiting_bulk_recheck")
	if !bulk && len(spec.Targets) == 0 {
		q.Reason = "waiting_probe_target"
		q.LastAttempt = now
		r.mu.Unlock()
		r.applyMu.Unlock()
		return
	}
	q.InFlight = true
	q.LastAttempt = now
	if bulk {
		q.LastBulk = now
	} else {
		q.LastHTTP = now
	}
	oldPresent := false
	for _, e := range r.entries {
		if e.route == plan.old {
			oldPresent = true
		}
	}
	oldAllowed := oldPresent && r.permitted(spec.Policy, plan.old.Config)
	oldEligible := oldAllowed && r.revisionReadyForAdmissionLocked(plan.old, now)
	plan.next.References.Add(1)
	if oldAllowed {
		plan.old.References.Add(1)
	}
	oldLoad, nextLoad := max(int64(0), plan.old.TrafficDownBPS.Load()), max(int64(0), plan.next.TrafficDownBPS.Load())
	oldFirst := len(q.Samples)%2 == 0
	target := spec.Speed
	if !bulk {
		target = spec.Targets[q.TargetIndex%len(spec.Targets)]
	}
	r.mu.Unlock()
	r.applyMu.Unlock()
	defer plan.next.References.Add(-1)
	if oldAllowed {
		defer plan.old.References.Add(-1)
	}
	perPath := qualityHTTPReserve
	if bulk {
		perPath = qualityBulkReserve
	}
	paths := int64(1)
	if oldAllowed {
		paths++
	}
	finish := func(reason string, pair *qualityPair) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.finishQualityAttemptLocked(plan, q, epoch, spec, time.Now()) {
			return
		}
		defer func() { r.refreshQualityVerdictLocked(plan, time.Now()) }()
		if ctx.Err() != nil || r.drained.Load() || time.Now().Unix() > r.lease.Load() || !r.permitted(spec.Policy, plan.next.Config) {
			q.Reason = "quality_authorization_changed"
			return
		}
		if pair != nil && oldAllowed && !r.permitted(spec.Policy, plan.old.Config) {
			q.Reason = "quality_authorization_changed"
			return
		}
		q.Reason = reason
		if pair != nil {
			// A broken primary control target must not permanently prevent a
			// healthy pair using an explicitly configured fallback. Never mix
			// percentiles from different targets in the same evidence window.
			if !pair.Bulk && !pair.Next.OK && !pair.Old.OK && len(spec.Targets) > 1 {
				q.TargetIndex = (q.TargetIndex + 1) % len(spec.Targets)
				q.Samples = nil
				q.LastBulk = time.Time{}
				q.Blocked = false
				q.Reason = "waiting_fallback_target"
				return
			}
			q.add(*pair)
			result := q.evaluate(time.Now(), spec.SpeedConfigured, spec.SpeedDisabled)
			q.Blocked = result.Pause
			q.Reason = result.Reason
		}
	}
	r.mu.Lock()
	latest, ok := r.qualitySpecLocked(plan)
	reserved := ok && r.qualityEpoch == epoch && latest.Signature == spec.Signature && r.rollovers[plan.next.Config.ID] == plan && r.permitted(spec.Policy, plan.next.Config) && r.reserveProbeLocked(now, spec.Policy, perPath*paths, latest.Budget)
	r.mu.Unlock()
	if !reserved {
		finish("waiting_budget", nil)
		return
	}
	a.mu.Lock()
	err := a.saveLocked()
	a.mu.Unlock()
	if err != nil {
		finish("waiting_budget_persistence", nil)
		return
	}
	// Restriction can arrive while durable storage is stalled. Never send a
	// measurement merely because its earlier reservation was authorized.
	r.mu.RLock()
	latest, ok = r.qualitySpecLocked(plan)
	allowed := ctx.Err() == nil && ok && r.qualityEpoch == epoch && latest.Signature == spec.Signature && r.rollovers[plan.next.Config.ID] == plan && r.permitted(spec.Policy, plan.next.Config) && (!oldAllowed || r.permitted(spec.Policy, plan.old.Config)) && !r.drained.Load() && time.Now().Unix() <= r.lease.Load()
	r.mu.RUnlock()
	if !allowed {
		finish("quality_authorization_changed", nil)
		return
	}
	call, cancel := context.WithTimeout(ctx, 2*spec.Timeout)
	defer cancel()
	pair := qualityPair{Bulk: bulk, OldEligible: oldEligible}
	measureOld := func() {
		if oldAllowed {
			pair.Old = measureRolloverHTTP(call, plan.old, target, bulk, spec.Timeout)
			pair.Old.BackgroundBPS = oldLoad
		}
	}
	measureNew := func() {
		pair.Next = measureRolloverHTTP(call, plan.next, target, bulk, spec.Timeout)
		pair.Next.BackgroundBPS = nextLoad
	}
	if oldFirst {
		measureOld()
		measureNew()
	} else {
		measureNew()
		measureOld()
	}
	pair.At = time.Now()
	finish("", &pair)
}
