package worker

import (
	"fmt"
	"proxyplatform/internal/config"
	"testing"
	"time"
)

func cachedQualityFixture(t *testing.T) (*Runtime, *pathRollover, time.Time) {
	t.Helper()
	r, p := qualityRuntimeFixture(t)
	now := time.Now()
	spec, ok := r.prepareQualityCacheLocked(p, now)
	if !ok {
		t.Fatal("missing spec")
	}
	p.quality = healthyQuality(now, 20)
	p.quality.Signature = spec.Signature
	r.prepareQualityCacheLocked(p, now)
	if !r.qualityVerdictLocked(p, now).Allow {
		t.Fatal("fixture not ready")
	}
	return r, p, now
}

func qualityPolicyConfig(r *Runtime) config.Config {
	c := *r.constraints
	c.Policies = append([]config.Policy(nil), c.Policies...)
	c.Routes = append([]config.Route(nil), c.Routes...)
	return c
}

func TestQualityCachePolicyEditImmediatelyHoldsTicketsAndPromotion(t *testing.T) {
	r, p, now := cachedQualityFixture(t)
	p.changed = now.Add(-31 * time.Second)
	p.started = now.Add(-60 * time.Second)
	p.success = 20
	c := qualityPolicyConfig(r)
	c.Policies[0].ProbeURL = "https://changed.example/health"
	r.Restrict(c)
	if r.qualityVerdictLocked(p, now).Allow {
		t.Fatal("stale policy cache admitted")
	}
	for range 100 {
		if r.chooseRevisionLocked(p.next, now) != p.old {
			t.Fatal("stale cache selected candidate")
		}
	}
	r.advanceRolloversLocked(now)
	if p.step != 0 || p.state != "canary" {
		t.Fatal("stale policy promoted", p.state, p.step)
	}
	r.prepareQualityCacheLocked(p, now)
	if p.quality != nil || r.qualityVerdictLocked(p, now).Allow {
		t.Fatal("different target reused samples")
	}
}

func TestQualityCacheStageExpiryAndUnrelatedGuardPreserveSamples(t *testing.T) {
	r, p, now := cachedQualityFixture(t)
	q := p.quality
	p.step++
	p.serial++
	c := qualityPolicyConfig(r)
	c.Generation++
	c.Routes[0].ValidUntil = c.Routes[0].ValidUntil.Add(time.Hour)
	r.Restrict(c)
	if r.qualityVerdictLocked(p, now).Allow {
		t.Fatal("unrefreshed epoch accepted")
	}
	r.prepareQualityCacheLocked(p, now)
	if p.quality != q || len(q.Samples) != 20 || !r.qualityVerdictLocked(p, now).Allow {
		t.Fatal("unrelated generation/expiry destroyed evidence")
	}
	guard := config.LiveGuard{Version: 1, Routes: map[string]config.RoutePermit{p.next.Config.Profile(): {Fingerprint: p.next.Config.Fingerprint, ValidUntil: p.next.Config.ValidUntil, Previous: append([]config.RevisionPermit(nil), p.next.Config.Previous...)}}}
	r.RestrictLiveGuard(guard)
	r.prepareQualityCacheLocked(p, now)
	guard = guard.Clone()
	guard.Version++
	guard.Routes["unrelated"] = config.RoutePermit{Fingerprint: "44444444444444444444444444444444", ValidUntil: now.Add(time.Hour)}
	r.RestrictLiveGuard(guard)
	if r.qualityVerdictLocked(p, now).Allow {
		t.Fatal("guard epoch did not invalidate cache")
	}
	r.prepareQualityCacheLocked(p, now)
	if p.quality != q || !r.qualityVerdictLocked(p, now).Allow {
		t.Fatal("unrelated guard reset samples")
	}
	if r.qualityVerdictLocked(p, now.Add(61*time.Second)).Allow || r.qualityVerdictLocked(p, now.Add(-time.Hour)).Allow {
		t.Fatal("stale/future cached verdict accepted")
	}
}

func TestQualityFinishAlwaysClearsInFlightAndRejectsEpochABA(t *testing.T) {
	for _, aba := range []bool{false, true} {
		t.Run(fmt.Sprint(aba), func(t *testing.T) {
			r, p, now := cachedQualityFixture(t)
			q := p.quality
			q.InFlight = true
			epoch := r.qualityEpoch
			spec := p.qualityCache.Spec
			original := qualityPolicyConfig(r)
			changed := qualityPolicyConfig(r)
			changed.Policies[0].ProbeURL = "https://changed.example/health"
			r.Restrict(changed)
			if aba {
				r.Restrict(original)
			}
			if r.finishQualityAttemptLocked(p, q, epoch, spec, now) {
				t.Fatal("epoch change accepted in-flight result")
			}
			if q.InFlight {
				t.Fatal("stale attempt permanently held probe slot")
			}
			if aba && (p.quality != q || len(q.Samples) != 20) {
				t.Fatal("unchanged effective signature lost earlier evidence")
			}
		})
	}
}

func TestQualityRevocationCannotRestorePreRevocationEvidence(t *testing.T) {
	for _, kind := range []string{"policy", "old_grant", "guard"} {
		t.Run(kind, func(t *testing.T) {
			r, p, now := cachedQualityFixture(t)
			q := p.quality
			q.InFlight = true
			epoch := r.qualityEpoch
			spec := p.qualityCache.Spec
			original := qualityPolicyConfig(r)
			switch kind {
			case "policy":
				changed := qualityPolicyConfig(r)
				changed.Policies[0].Routes = nil
				r.Restrict(changed)
				r.Restrict(original)
			case "old_grant":
				changed := qualityPolicyConfig(r)
				changed.Routes[0].Previous = nil
				r.Restrict(changed)
				r.Restrict(original)
			case "guard":
				good := config.LiveGuard{Version: 1, Routes: map[string]config.RoutePermit{p.next.Config.Profile(): {Fingerprint: p.next.Config.Fingerprint, ValidUntil: p.next.Config.ValidUntil, Previous: append([]config.RevisionPermit(nil), p.next.Config.Previous...)}}}
				r.RestrictLiveGuard(good)
				r.prepareQualityCacheLocked(p, now)
				epoch = r.qualityEpoch
				r.RestrictLiveGuard(config.LiveGuard{Version: 2, Routes: map[string]config.RoutePermit{}})
				good.Version = 3
				r.RestrictLiveGuard(good)
			}
			if p.quality != nil {
				t.Fatal("revoked evidence retained")
			}
			if r.finishQualityAttemptLocked(p, q, epoch, spec, now) || q.InFlight {
				t.Fatal("pre-revocation result restored or leaked InFlight")
			}
			r.prepareQualityCacheLocked(p, now)
			if r.qualityVerdictLocked(p, now).Allow {
				t.Fatal("regrant restored stale quality authority")
			}
		})
	}
}

// Compare the same ticket path with and without the removed JSON/hash/catalogue
// work. This measures this optimization, not overall worker/WAN overhead vs a
// released binary; the latter remains the independent SPEC performance gate.
func BenchmarkRolloverQualityAdmission(b *testing.B) {
	for _, size := range []int{1, 2000} {
		for _, legacy := range []bool{false, true} {
			b.Run(fmt.Sprintf("policies%d/recompute%v", size, legacy), func(b *testing.B) {
				r, old, next := rolloverFixture()
				old.Config.CheckServer = true
				next.Config.CheckServer = true
				old.Instance.ProbeSamples.Store(3)
				next.Instance.ProbeSamples.Store(3)
				c := r.config
				c.Routes = []config.Route{next.Config}
				c.Policies[0].DisableSpeedProbes = true
				c.Policies[0].ProbeURL = "https://controlled.example/health"
				for i := 1; i < size; i++ {
					c.Policies = append(c.Policies, config.Policy{ID: fmt.Sprintf("unrelated%d", i), Routes: []string{"different"}})
				}
				r.config = c
				r.Restrict(c)
				p := r.rollovers["a"]
				p.step = 3
				p.serial = 1
				p.durableSerial = 1
				now := time.Now()
				spec, ok := r.prepareQualityCacheLocked(p, now)
				if !ok {
					b.Fatal("no spec")
				}
				p.quality = healthyQuality(now, 20)
				p.quality.Signature = spec.Signature
				r.prepareQualityCacheLocked(p, now)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if legacy {
						r.mu.Lock()
						s, _ := r.qualitySpecLocked(p)
						_ = p.quality.evaluate(time.Now(), s.SpeedConfigured, s.SpeedDisabled)
						r.mu.Unlock()
					}
					ticket, err := r.reserveRoute("policy:p", next)
					if err != nil {
						b.Fatal(err)
					}
					ticket.release()
				}
			})
		}
	}
}
