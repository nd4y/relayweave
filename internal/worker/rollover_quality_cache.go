package worker

import (
	"proxyplatform/internal/config"
	"time"
)

type qualityAuthority struct {
	Current         string
	Until           time.Time
	Previous        []config.RevisionPermit
	EstablishedOnly bool
}

func (a qualityAuthority) permits(fp string, now time.Time) bool {
	if fp == "" || (!a.Until.IsZero() && !now.Before(a.Until)) {
		return false
	}
	if a.Current == fp {
		return true
	}
	for _, p := range a.Previous {
		if p.Fingerprint == fp && now.Before(p.ValidUntil) {
			return true
		}
	}
	return false
}

type rolloverQualityCache struct {
	Epoch              uint64
	Valid              bool
	Spec               rolloverQualitySpec
	Quality            *rolloverQuality
	Verdict            qualityVerdict
	EvaluatedAt, Until time.Time
}

// Restrict has already prepared route/policy maps outside the admission mutex.
// This bounded live-plan pass performs no JSON, hashing, target lookup or I/O.
// Loss of relevant authority also destroys previous evidence, preventing an
// A->revoke->A sequence from restoring pre-revocation trust.
func (r *Runtime) invalidateQualityConfigLocked(allowed map[string]map[string]string, authority map[string]qualityAuthority) {
	r.qualityEpoch++
	now := time.Now()
	for id, p := range r.rollovers {
		if p.quality == nil {
			continue
		}
		policy := p.qualityCache.Spec.Policy
		if r.allowed != nil && r.allowed[policy][id] != allowed[policy][id] {
			p.quality = nil
			continue
		}
		before, after := r.qualityAuthority[id], authority[id]
		if (!before.EstablishedOnly && after.EstablishedOnly) ||
			(before.permits(p.next.Config.Fingerprint, now) && !after.permits(p.next.Config.Fingerprint, now)) ||
			(before.permits(p.old.Config.Fingerprint, now) && !after.permits(p.old.Config.Fingerprint, now)) {
			p.quality = nil
		}
	}
	r.qualityAuthority = authority
}

// Receiver critical section: only an epoch increment and at most32 plans, each
// with direct map lookups and <=2 frozen grants. Never compute a probe signature.
func (r *Runtime) invalidateQualityGuardLocked(next config.LiveGuard) {
	r.qualityEpoch++
	now := time.Now()
	for _, p := range r.rollovers {
		if p.quality == nil {
			continue
		}
		for _, v := range []*Route{p.next, p.old} {
			after, exists := next.Routes[v.Config.Profile()]
			before, was := config.RoutePermit{}, false
			if r.liveGuard != nil {
				before, was = r.liveGuard.Routes[v.Config.Profile()]
			}
			wasAllowed := r.liveGuard == nil || (was && before.Permits(v.Config.Fingerprint, now, true))
			if (wasAllowed && (!exists || !after.Permits(v.Config.Fingerprint, now, true))) ||
				(!before.EstablishedOnly && after.EstablishedOnly) ||
				(before.ServerPolicyDigest != after.ServerPolicyDigest) {
				p.quality = nil
				break
			}
		}
	}
}

// Probe/control path only. Expiry extensions, stages and unrelated source edits
// can change the epoch while preserving the effective signature and samples.
func (r *Runtime) prepareQualityCacheLocked(p *pathRollover, now time.Time) (rolloverQualitySpec, bool) {
	if !p.qualityCache.Valid || p.qualityCache.Epoch != r.qualityEpoch {
		spec, ok := r.qualitySpecLocked(p)
		p.qualityCache = rolloverQualityCache{Epoch: r.qualityEpoch, Valid: ok, Spec: spec}
		if !ok {
			return spec, false
		}
		if p.quality != nil && p.quality.Signature != spec.Signature {
			p.quality = nil
		}
	}
	r.refreshQualityVerdictLocked(p, now)
	return p.qualityCache.Spec, true
}

func (r *Runtime) refreshQualityVerdictLocked(p *pathRollover, now time.Time) {
	c := &p.qualityCache
	if !c.Valid || c.Epoch != r.qualityEpoch || p.quality == nil || p.quality.Signature != c.Spec.Signature {
		return
	}
	c.Quality = p.quality
	c.EvaluatedAt = now
	c.Until = now.Add(qualityWindow)
	c.Verdict = p.quality.evaluate(now, c.Spec.SpeedConfigured, c.Spec.SpeedDisabled)
	for _, s := range p.quality.Samples {
		if !s.At.IsZero() && !s.At.After(now) && now.Sub(s.At) <= qualityWindow {
			c.Until = minTime(c.Until, s.At.Add(qualityWindow))
		}
	}
	if !c.Verdict.Allow && !c.Verdict.Pause && p.quality.Reason != "" {
		c.Verdict.Reason = p.quality.Reason
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Ticket/stage path: O(1), no catalogue scan, marshal, SHA, allocation, percentile
// sorting or mutation. Expired/stale caches hold admission until the probe path
// refreshes them; an old control result cannot extend its own evidence window.
func (r *Runtime) qualityVerdictLocked(p *pathRollover, now time.Time) qualityVerdict {
	c := &p.qualityCache
	if !c.Valid || c.Epoch != r.qualityEpoch || c.Quality != p.quality || p.quality == nil || now.Before(c.EvaluatedAt) || !now.Before(c.Until) {
		return qualityVerdict{Reason: "waiting_quality_samples", Mode: "evaluating", Confidence: "insufficient", BulkState: "waiting"}
	}
	return c.Verdict
}

// Always release InFlight, including a changed target or A->B->A during I/O.
// Epoch changes invalidate this result even when the eventual signature agrees.
func (r *Runtime) finishQualityAttemptLocked(p *pathRollover, q *rolloverQuality, epoch uint64, spec rolloverQualitySpec, now time.Time) bool {
	q.InFlight = false
	if r.rollovers[p.next.Config.ID] != p || p.quality != q {
		return false
	}
	latest, ok := r.prepareQualityCacheLocked(p, now)
	if !ok || p.quality != q || r.qualityEpoch != epoch || latest.Signature != spec.Signature {
		return false
	}
	return true
}
