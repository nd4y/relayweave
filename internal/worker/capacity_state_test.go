package worker

import (
	"strings"
	"sync"
	"testing"
	"time"

	"proxyplatform/internal/config"
)

func classifiedBudgetFixture() *Runtime {
	return &Runtime{budget: map[string]int64{}, capacityBudget: map[string]int64{},
		probeWindow: time.Now().UTC().Truncate(24 * time.Hour),
		config:      config.Config{Policies: []config.Policy{{ID: "p", ProbeBudgetBytes: 128 << 20}, {ID: "q", ProbeBudgetBytes: 128 << 20}}}}
}

func TestQualitySpendingDoesNotConsumeCapacityShareOrExpandGlobalBudget(t *testing.T) {
	r := classifiedBudgetFixture()
	now := time.Now()
	if !r.reserveProbe(now, "p", 60<<20, 128<<20) {
		t.Fatal("quality reservation")
	}
	for range 8 {
		if !r.reserveCapacityProbe(now, "q", 8<<20, 128<<20, true) {
			t.Fatal("quality spending starved the separate capacity allowance")
		}
	}
	if r.capacityReservedLocked(now) != 64<<20 || r.reservedProbeBudgetLocked() != 124<<20 {
		t.Fatal("classified reservations lost global accounting")
	}
	if r.reserveCapacityProbe(now, "q", 1, 128<<20, true) {
		t.Fatal("capacity probes borrowed the health half")
	}
	if !r.reserveProbe(now, "p", 4<<20, 128<<20) || r.reserveProbe(now, "p", 1, 128<<20) {
		t.Fatal("global cap changed")
	}
	if r.capacityBudgetStatusLocked(now)["capacity_probe_state"] != "global_budget_exhausted" {
		t.Fatal("status hides global exhaustion")
	}
}

func TestClassifiedBudgetPreservesPolicyLimitsConcurrentReservationsAndUTCReset(t *testing.T) {
	r := classifiedBudgetFixture()
	now := time.Now()
	r.config.Policies[0].ProbeBudgetBytes = 16 << 20
	if !r.reserveProbe(now, "p", 12<<20, 128<<20) || r.reserveCapacityProbe(now, "p", 8<<20, 128<<20, true) {
		t.Fatal("purpose accounting bypassed per-policy cap")
	}
	var done sync.WaitGroup
	for range 32 {
		done.Add(1)
		go func() { defer done.Done(); r.reserveCapacityProbe(now, "q", 8<<20, 128<<20, true) }()
	}
	done.Wait()
	if r.capacityReservedLocked(now) != 64<<20 || r.reservedProbeBudgetLocked() != 76<<20 {
		t.Fatal("concurrent capacity allowance overspent")
	}
	if !r.reserveCapacityProbe(now.Add(24*time.Hour), "q", 8<<20, 128<<20, true) || r.budget["p"] != 0 {
		t.Fatal("UTC rollover did not reset both purposes")
	}
}

func TestLegacyBudgetClassificationNeverRefundsUnknownReservations(t *testing.T) {
	now := time.Now()
	r := classifiedBudgetFixture()
	r.restoreProbeBudget(now.UTC().Truncate(24*time.Hour), map[string]int64{"p": 60 << 20}, nil, 0)
	if r.reserveCapacityProbe(now, "q", 8<<20, 128<<20, true) || r.budget["p"] != 60<<20 {
		t.Fatal("legacy upgrade invented spare capacity budget")
	}
	if !r.reserveProbe(now, "q", 8<<20, 128<<20) {
		t.Fatal("capacity limit disabled ordinary health reservations")
	}
	if r.capacityReservedLocked(now) != 60<<20 || r.reservedProbeBudgetLocked() != 68<<20 {
		t.Fatal("purpose attribution changed a legacy total")
	}
}

func capacityCacheFixture() (*engineEntry, config.Config) {
	e := &engineEntry{key: "route-core-content-dial-IP", route: &Route{Config: config.Route{ID: "a", EgressID: "isp1"}}}
	c := config.Config{EgressPaths: []config.EgressPath{{ID: "isp1", SourceIP: "192.0.2.1"}},
		Policies: []config.Policy{{ID: "p", Routes: []string{"a"}, SpeedURL: "https://reference.invalid/bulk"}}}
	return e, c
}

func TestCapacityCacheBindsCoreDialIPSourceTargetAndExpiryWithoutGrantingReadiness(t *testing.T) {
	e, c := capacityCacheFixture()
	now := time.Now()
	r := classifiedBudgetFixture()
	r.rememberCapacityLocked(e, c, c.Policies[0].SpeedURL, 25_000_000, now)
	for _, change := range []string{"none", "engine", "source", "target", "disabled", "expired", "future"} {
		t.Run(change, func(t *testing.T) {
			candidate, cfg := capacityCacheFixture()
			at := now
			switch change {
			case "engine":
				candidate.key += "-different-core-or-checked-IP"
			case "source":
				cfg.EgressPaths[0].SourceIP = "192.0.2.2"
			case "target":
				cfg.Policies[0].SpeedURL += "?other=1"
			case "disabled":
				cfg.Policies[0].DisableSpeedProbes = true
			case "expired":
				at = now.Add(capacityHintLifetime)
			case "future":
				at = now.Add(-time.Second)
			}
			r.restoreEntryCapacityLocked(candidate, cfg, at)
			if (candidate.route.SpeedBPS.Load() == 25_000_000) != (change == "none") {
				t.Fatal("capacity hint crossed its identity/freshness boundary")
			}
			if candidate.route.Instance != nil || candidate.route.UDPHealthy.Load() {
				t.Fatal("advisory cache granted health or admission")
			}
		})
	}
}

func TestClassifiedBudgetAndCapacityHintsSurviveEncryptedRestart(t *testing.T) {
	a := &Agent{dir: t.TempDir()}
	if err := a.load(); err != nil {
		t.Fatal(err)
	}
	a.runtime = classifiedBudgetFixture()
	now := time.Now()
	if !a.runtime.reserveProbe(now, "p", 60<<20, 128<<20) || !a.runtime.reserveCapacityProbe(now, "q", 8<<20, 128<<20, true) {
		t.Fatal("fixture reservations")
	}
	e, c := capacityCacheFixture()
	a.runtime.rememberCapacityLocked(e, c, c.Policies[0].SpeedURL, 25_000_000, now)
	if err := a.saveLocked(); err != nil {
		t.Fatal(err)
	}
	b := &Agent{dir: a.dir}
	if err := b.load(); err != nil {
		t.Fatal(err)
	}
	r := classifiedBudgetFixture()
	r.restoreProbeBudget(b.state.ProbeDay, b.state.ProbeBudget, b.state.ProbeCapacityBudget, b.state.ProbeBudgetVersion)
	r.capacityHints = b.state.CapacityHints
	if r.reservedProbeBudgetLocked() != 68<<20 || r.capacityReservedLocked(now) != 8<<20 || r.probeBytes.Load() != 0 {
		t.Fatal("restart erased/rescaled reservations or confused them with bytes read")
	}
	e, c = capacityCacheFixture()
	r.restoreEntryCapacityLocked(e, c, now)
	if e.route.SpeedBPS.Load() != 25_000_000 || e.route.LastSpeed.Load() != now.Unix() {
		t.Fatal("restart lost the still-fresh measured estimate")
	}
	if !r.reserveCapacityProbe(now, "q", 8<<20, 128<<20, true) || r.reservedProbeBudgetLocked() != 76<<20 {
		t.Fatal("restart starved capacity or refunded shared spending")
	}
}

func TestCapacityHintCacheIsBoundedAndRejectsMalformedObservations(t *testing.T) {
	r := classifiedBudgetFixture()
	e, c := capacityCacheFixture()
	now := time.Now()
	for i := range processLimit * 3 {
		e.key = strings.Repeat("x", i+1)
		r.rememberCapacityLocked(e, c, c.Policies[0].SpeedURL, 100, now)
	}
	if len(r.capacityHints) != processLimit*2 {
		t.Fatal("unbounded advisory cache")
	}
	for _, h := range []capacityHint{{BPS: -1, At: now.Unix()}, {BPS: 1, At: now.Add(time.Second).Unix()}, {BPS: 1, At: 0}} {
		if validCapacityHint(strings.Repeat("0", 64), h, now) {
			t.Fatal("invalid hint accepted")
		}
	}
	if validCapacityBudget(map[string]int64{"p": 10}, map[string]int64{"p": 11}) || validCapacityBudget(map[string]int64{"p": -1}, nil) {
		t.Fatal("metadata accepted impossible reservation accounting")
	}
}
