package worker

import (
	"proxyplatform/internal/config"
	"testing"
	"time"
)

func TestQualityPolicyPrefersConfiguredAuthorizedPairRegardlessOfOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		r, p := qualityRuntimeFixture(t)
		c := qualityPolicyConfig(r)
		c.ProbeURL = "https://controlled.example/health"
		pin := config.Policy{ID: "a-pin", Routes: []string{"a"}, ProbeBudgetBytes: 128 << 20}
		auto := config.Policy{ID: "z-auto", Routes: []string{"a"}, Mode: "auto", SpeedURL: "https://controlled.example/bulk", ProbeBudgetBytes: 128 << 20}
		c.Policies = []config.Policy{pin, auto}
		if reverse {
			c.Policies = []config.Policy{auto, pin}
		}
		r.Restrict(c)
		spec, ok := r.qualitySpecLocked(p)
		if !ok || spec.Policy != "z-auto" || !spec.SpeedConfigured || spec.SpeedDisabled {
			t.Fatal("empty pin policy blocked configured auto measurement", reverse, spec)
		}
		// A policy that has stopped authorizing this route cannot provide either
		// its target or its budget to another policy's measurements.
		auto.Routes = nil
		c.Policies = []config.Policy{pin, auto}
		r.Restrict(c)
		spec, ok = r.qualitySpecLocked(p)
		if !ok || spec.Policy != "a-pin" || spec.SpeedConfigured || spec.SpeedDisabled {
			t.Fatal("unauthorized target borrowed or empty URL treated as disable", spec)
		}
	}
}

func TestQualityPolicyExplicitDisableAndConfiguredCapsHaveStableSemantics(t *testing.T) {
	r, p := qualityRuntimeFixture(t)
	c := qualityPolicyConfig(r)
	c.ProbeURL = "https://controlled.example/health"
	disabled := config.Policy{ID: "a-disabled", Routes: []string{"a"}, DisableSpeedProbes: true, ProbeBudgetBytes: 128 << 20}
	enabled := config.Policy{ID: "z-enabled", Routes: []string{"a"}, SpeedURL: "https://controlled.example/bulk", ProbeBudgetBytes: 128 << 20}
	c.Policies = []config.Policy{disabled, enabled}
	r.Restrict(c)
	spec, _ := r.qualitySpecLocked(p)
	if spec.Policy != enabled.ID || spec.SpeedDisabled {
		t.Fatal("one disabled policy overrode separately authorized configured policy", spec)
	}
	enabled.ProbeBudgetBytes = 0
	c.Policies = []config.Policy{disabled, enabled}
	r.Restrict(c)
	spec, _ = r.qualitySpecLocked(p)
	if spec.Policy != disabled.ID || !spec.SpeedDisabled {
		t.Fatal("explicit disabled health policy unavailable", spec)
	}
	c.Policies = []config.Policy{enabled}
	r.Restrict(c)
	spec, _ = r.qualitySpecLocked(p)
	if spec.Policy != enabled.ID || spec.SpeedDisabled || !spec.SpeedConfigured || r.reserveProbe(time.Now(), spec.Policy, qualityBulkReserve, spec.Budget) {
		t.Fatal("missing budget bypassed or falsely disabled", spec)
	}
}

func TestQualityMeasurementPolicyAuthorityScopeIsPartOfSignature(t *testing.T) {
	r, p := qualityRuntimeFixture(t)
	c := qualityPolicyConfig(r)
	c.Policies[0].ID = "z-policy"
	r.Restrict(c)
	first, ok := r.qualitySpecLocked(p)
	if !ok {
		t.Fatal("no spec")
	}
	other := c.Policies[0]
	other.ID = "a-policy"
	c.Policies = append(c.Policies, other)
	r.Restrict(c)
	second, ok := r.qualitySpecLocked(p)
	if !ok || second.Policy != "a-policy" || first.Signature == second.Signature {
		t.Fatal("changed policy authority/budget scope retained signature", first, second)
	}
	c.Policies[1].ProbeURL = "https://different.example/health"
	r.Restrict(c)
	third, _ := r.qualitySpecLocked(p)
	if third.Signature == second.Signature {
		t.Fatal("effective target change retained signature")
	}
}
