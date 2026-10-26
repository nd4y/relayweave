package worker

import (
	"context"
	"fmt"
	"proxyplatform/internal/config"
	"testing"
	"time"
)

func udpScheduleFixture(count int, budget int64, now time.Time) *Runtime {
	r := &Runtime{ctx: context.Background(), current: map[string]*engineEntry{}, policies: map[string]*policyState{}}
	p := &policyState{config: config.Policy{ID: "p", ProbeBudgetBytes: budget}}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("route-%02d", i)
		v := readyRoute(id)
		v.Config.CheckServer, v.Config.UDP = true, true
		r.current[id] = &engineEntry{route: v}
		p.config.Routes = append(p.config.Routes, id)
	}
	r.policies["p"] = p
	r.RenewLease(now.Add(time.Hour))
	return r
}

// Uses the exact production selection/reservation/completion functions with an
// injected clock. No direct UDPAttempt resets or fabricated three-check flags.
func TestUDPProductionScheduleWarmsAndMaintainsTwelvePaths(t *testing.T) {
	start := time.Unix(1800000000, 0).UTC()
	r := udpScheduleFixture(12, 256<<20, start)
	first, all := -1, -1
	probes := 0
	for second := 0; second <= 600; second++ {
		now := start.Add(time.Duration(second) * time.Second)
		r.mu.Lock()
		choice := r.reserveUDPProbeLocked(now)
		r.mu.Unlock()
		if choice.route != nil {
			if !r.reserveProbe(now, choice.policy, 2048, choice.budget) {
				t.Fatal("unexpected budget exhaustion")
			}
			recordUDPProbe(choice.route, true, now)
			choice.route.References.Add(-1)
			probes++
		}
		ready := 0
		for _, e := range r.current {
			if udpProvenAt(e.route, now) {
				ready++
			}
		}
		if ready > 0 && first < 0 {
			first = second
		}
		if ready == 12 && all < 0 {
			all = second
		}
		if all >= 0 && ready != 12 {
			t.Fatalf("validated paths expired at second%d: %d", second, ready)
		}
	}
	if first > 3 || first < 0 || all > 40 || all < 0 {
		t.Fatalf("slow bootstrap: first=%d all=%d", first, all)
	}
	if probes > 12*(3+600/45+1) {
		t.Fatalf("unbounded probing: %d", probes)
	}
	for _, e := range r.current {
		if e.route.References.Load() != 0 {
			t.Fatal("probe reference leaked")
		}
	}
}

func TestUDPProductionScheduleHonorsBudgetFailureAndStaleWarmup(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	r := udpScheduleFixture(12, 4096, now)
	for second := 0; second < 40; second++ {
		at := now.Add(time.Duration(second) * time.Second)
		choice := r.reserveUDPProbeLocked(at)
		if choice.route == nil {
			continue
		}
		if r.reserveProbe(at, choice.policy, 2048, choice.budget) {
			recordUDPProbe(choice.route, true, at)
		}
		choice.route.References.Add(-1)
		for _, e := range r.current {
			if udpProvenAt(e.route, at) {
				t.Fatal("exhausted budget fabricated readiness")
			}
		}
	}
	v := readyRoute("stale")
	v.Config.CheckServer = true
	for second := 0; second < 3; second++ {
		recordUDPProbe(v, true, now.Add(time.Duration(second)*time.Second))
	}
	if !udpProvenAt(v, now.Add(3*time.Second)) {
		t.Fatal("warmup failed")
	}
	recordUDPProbe(v, true, now.Add(100*time.Second))
	if udpProvenAt(v, now.Add(100*time.Second)) || v.UDPChecks.Load() != 1 {
		t.Fatal("stale evidence bypassed new warmup")
	}
	recordUDPProbe(v, false, now.Add(101*time.Second))
	if v.UDPChecks.Load() != 0 || udpProvenAt(v, now.Add(101*time.Second)) {
		t.Fatal("failure left UDP ready")
	}
}

func TestUDPProductionScheduleMaintainsHealthyPathsPastFailedPeer(t *testing.T) {
	start := time.Unix(1800000000, 0).UTC()
	r := udpScheduleFixture(12, 256<<20, start)
	badAttempts := 0
	for second := 0; second <= 600; second++ {
		now := start.Add(time.Duration(second) * time.Second)
		choice := r.reserveUDPProbeLocked(now)
		if choice.route != nil {
			ok := choice.route.Config.ID != "route-00"
			if !ok {
				badAttempts++
			}
			recordUDPProbe(choice.route, ok, now)
			choice.route.References.Add(-1)
		}
		if second >= 45 {
			for id, e := range r.current {
				if id != "route-00" && !udpProvenAt(e.route, now) {
					t.Fatalf("failed peer starved %s at%d", id, second)
				}
			}
		}
	}
	if badAttempts > 21 {
		t.Fatalf("failure retry storm: %d", badAttempts)
	}
}

func TestUDPProductionScheduleMaintainsOldFallbackUntilReplacementIsReady(t *testing.T) {
	start := time.Unix(1800000000, 0).UTC()
	r := udpScheduleFixture(1, 256<<20, start)
	next := r.current["route-00"].route
	old := readyRoute("route-00")
	old.Config.CheckServer, old.Config.UDP = true, true
	old.Config.Endpoint = "old"
	next.Config.Endpoint = "next"
	for i := 3; i > 0; i-- {
		recordUDPProbe(old, true, start.Add(-time.Duration(i)*time.Second))
	}
	r.rollovers = map[string]*pathRollover{"route-00": {old: old, next: next, state: "active_draining"}}
	for second := 0; second <= 180; second++ {
		now := start.Add(time.Duration(second) * time.Second)
		choice := r.reserveUDPProbeLocked(now)
		if choice.route != nil {
			recordUDPProbe(choice.route, choice.route == old, now)
			choice.route.References.Add(-1)
		}
		if !udpProvenAt(old, now) {
			t.Fatalf("old UDP fallback expired at%d", second)
		}
	}
	for i := 181; i <= 183; i++ {
		recordUDPProbe(next, true, start.Add(time.Duration(i)*time.Second))
	}
	if choice := r.reserveUDPProbeLocked(start.Add(184 * time.Second)); choice.route != nil {
		choice.route.References.Add(-1)
		if choice.route == old {
			t.Fatal("obsolete fallback kept consuming probe budget")
		}
	}
}
