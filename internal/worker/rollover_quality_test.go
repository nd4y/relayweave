package worker

import (
	"proxyplatform/internal/config"
	"testing"
	"time"
)

func healthyQuality(now time.Time, n int) *rolloverQuality {
	q := &rolloverQuality{}
	for i := range n {
		q.add(qualityPair{At: now.Add(-time.Duration(n-i) * time.Second), OldEligible: true, Old: qualityMeasurement{OK: true, ConnectUS: 10000, TTFBUS: 30000}, Next: qualityMeasurement{OK: true, ConnectUS: 12000, TTFBUS: 32000}})
	}
	return q
}

func TestPairedQualityNeedsActualControlsAndExplicitBulkPolicy(t *testing.T) {
	now := time.Now()
	q := healthyQuality(now, 2)
	if v := q.evaluate(now, false, true); v.Allow || v.Pause {
		t.Fatal("two controls accepted", v)
	}
	q = healthyQuality(now, 3)
	if v := q.evaluate(now, false, false); v.Allow || v.Reason != "waiting_speed_target" {
		t.Fatal("missing target faked a bulk pass", v)
	}
	v := q.evaluate(now, false, true)
	if !v.Allow || v.Confidence != "limited_sample_guard" || v.BulkState != "disabled_by_policy" {
		t.Fatal(v)
	}
	if v = q.evaluate(now.Add(61*time.Second), false, true); v.Allow {
		t.Fatal("stale evidence reused", v)
	}
	if v = q.evaluate(now.Add(-time.Hour), false, true); v.Allow {
		t.Fatal("future evidence reused", v)
	}
}

func TestPairedLatencyAndBulkRegressionHoldBeforeFurtherPromotion(t *testing.T) {
	now := time.Now()
	q := healthyQuality(now, 20)
	for i := range q.Samples {
		q.Samples[i].Next.TTFBUS = 51000
	}
	if v := q.evaluate(now, true, false); !v.Pause || v.Reason != "paired_latency_regression" {
		t.Fatal(v)
	}
	q = healthyQuality(now, 20)
	pair := qualityPair{At: now, Bulk: true, OldEligible: true, Old: qualityMeasurement{OK: true, Bytes: qualityBulkPayload, ElapsedUS: 100000, BackgroundBPS: 500000}, Next: qualityMeasurement{OK: true, Bytes: qualityBulkPayload, ElapsedUS: 120000, BackgroundBPS: 500000}}
	q.add(pair)
	if v := q.evaluate(now, true, false); v.Allow || v.Pause || v.Reason != "waiting_bulk_recheck" {
		t.Fatal(v)
	}
	q.add(pair)
	if v := q.evaluate(now, true, false); !v.Pause || v.Reason != "paired_bulk_regression" {
		t.Fatal(v)
	}
	pair.Next.ElapsedUS = 105000
	q.add(pair)
	if v := q.evaluate(now, true, false); !v.Allow || v.BulkState != "paired_lower_bound" {
		t.Fatal(v)
	}
	pair.Next.OK = false
	q.add(pair)
	if v := q.evaluate(now, true, false); v.Allow {
		t.Fatal("latest failed burst ignored", v)
	}
	q = healthyQuality(now, 20)
	pair.Next.OK = true
	pair.Next.BackgroundBPS = 5000000
	q.add(pair)
	if v := q.evaluate(now, true, false); v.Allow || v.Reason != "waiting_comparable_bulk" {
		t.Fatal(v)
	}
}

func TestQualityVerifiedRecoveryHasAbsoluteProofWithoutRelativeClaim(t *testing.T) {
	now := time.Now()
	q := healthyQuality(now, 3)
	for i := range q.Samples {
		q.Samples[i].OldEligible = false
		q.Samples[i].Old.OK = false
	}
	if v := q.evaluate(now, false, true); !v.Allow || v.Mode != "verified_recovery" || v.Reason != "baseline_unavailable" {
		t.Fatal(v)
	}
	if v := q.evaluate(now, true, false); v.Allow {
		t.Fatal("unmeasured bulk accepted", v)
	}
	pair := qualityPair{At: now, Bulk: true, Next: qualityMeasurement{OK: true, Bytes: qualityBulkPayload, ElapsedUS: 500000}}
	q.add(pair)
	if v := q.evaluate(now, true, false); !v.Allow || v.BulkState != "baseline_unavailable" || v.BulkRatio != 0 {
		t.Fatal(v)
	}
	pair.Next.OK = false
	q.add(pair)
	if v := q.evaluate(now, true, false); v.Allow {
		t.Fatal("failed recovery burst ignored", v)
	}
}

func qualityRuntimeFixture(t *testing.T) (*Runtime, *pathRollover) {
	t.Helper()
	r, old, next := rolloverFixture()
	old.Config.CheckServer = true
	next.Config.CheckServer = true
	old.Instance.ProbeSamples.Store(3)
	next.Instance.ProbeSamples.Store(3)
	c := r.config
	c.Routes = []config.Route{next.Config}
	c.Policies[0].ProbeURL = "https://controlled.example/health"
	c.Policies[0].DisableSpeedProbes = true
	c.Policies[0].ProbeBudgetBytes = 8 << 20
	r.config = c
	r.Restrict(c)
	p := r.rollovers["a"]
	p.serial = 1
	p.durableSerial = 1
	return r, p
}

func TestMissingBaselineCannotBypassQualityCohortAndCaps(t *testing.T) {
	r, p := qualityRuntimeFixture(t)
	now := time.Now()
	p.old.Instance.Ready.Store(false)
	for range 100 {
		if ticket, err := r.reserveRoute("policy:p", p.next); err == nil {
			ticket.release()
			t.Fatal("unmeasured candidate got ticket")
		}
	}
	spec, ok := r.qualitySpecLocked(p)
	if !ok {
		t.Fatal("no spec")
	}
	p.quality = healthyQuality(now, 3)
	p.quality.Signature = spec.Signature
	for i := range p.quality.Samples {
		p.quality.Samples[i].OldEligible = false
		p.quality.Samples[i].Old.OK = false
	}
	r.prepareQualityCacheLocked(p, now)
	count := 0
	for range 100 {
		if ticket, err := r.reserveRoute("policy:p", p.next); err == nil {
			count++
			ticket.release()
		}
	}
	if count != 1 {
		t.Fatal("missing baseline bypassed first-stage cap", count)
	}
	p.quality.Blocked = true
	if ticket, err := r.reserveRoute("policy:p", p.next); err == nil {
		ticket.release()
		t.Fatal("quality failure bypassed at ticket boundary")
	}
}

func TestQualitySignatureIgnoresStageAndExpiryButResetsOnPolicyOrPin(t *testing.T) {
	r, p := qualityRuntimeFixture(t)
	first, _ := r.qualitySpecLocked(p)
	p.step++
	p.serial++
	p.next.Config.ValidUntil = p.next.Config.ValidUntil.Add(time.Hour)
	second, _ := r.qualitySpecLocked(p)
	if first.Signature != second.Signature {
		t.Fatal("stage or expiry reset evidence")
	}
	r.constraints.Policies[0].DisableSpeedProbes = false
	third, _ := r.qualitySpecLocked(p)
	if second.Signature == third.Signature {
		t.Fatal("policy change reused evidence")
	}
	r.entries["new"].dialIP = "203.0.113.25"
	fourth, _ := r.qualitySpecLocked(p)
	if third.Signature == fourth.Signature {
		t.Fatal("different socket reused evidence")
	}
}

func TestProbeReservationRespectsCurrentRestriction(t *testing.T) {
	r, _ := qualityRuntimeFixture(t)
	r.constraints.Policies[0].ProbeBudgetBytes = 1024
	if r.reserveProbe(time.Now(), "p", 2048, 8<<20) {
		t.Fatal("stale budget bypassed Restrict")
	}
}
