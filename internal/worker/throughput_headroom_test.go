package worker

import (
	"math"
	"testing"
	"time"
)

func TestOwnTransferIsNotCountedAsForeignWANLoad(t *testing.T) {
	r, p, routes := automaticFixture()
	for _, v := range routes {
		v.Active.Store(4)
	}
	r.entries = map[string]*engineEntry{"a": {route: routes[0]}, "b": {route: routes[1]}}
	set, refresh := attachTelemetry(t, r)
	now := time.Now()
	set(11_250_000, 0, false, now) // 90 Mbps on a 100 Mbps lane.
	refresh()
	foreign := allocate(r, p, routes, 1000)["a"]
	routes[0].TrafficDownBPS.Store(11_000_000)
	routes[0].TrafficAt.Store(now.Unix())
	r.ispObservations = nil
	p.egressCredit = nil
	own := allocate(r, p, routes, 1000)["a"]
	if foreign > 120 || own < 400 || own > 510 {
		t.Fatalf("background=%d own=%d; own payload was penalized twice or full load ignored", foreign, own)
	}
	a := r.ispAllocations["a"]
	if a.Background == nil || math.Abs(*a.Background-.02) > .001 || a.Utilization == nil || math.Abs(*a.Utilization-.9) > .001 || a.OwnDownBPS != 11_000_000 {
		t.Fatal("missing distinguishable own/background evidence", a)
	}
	// No old observation can manufacture spare capacity after a sampler stall.
	routes[0].TrafficAt.Store(now.Add(-10 * time.Second).Unix())
	r.ispObservations = nil
	p.egressCredit = nil
	if got := allocate(r, p, routes, 1000)["a"]; got > 120 {
		t.Fatal("stale counters hid foreign traffic", got)
	}
}

func TestOwnISPAccountingIncludesDrainingButRejectsMismatchedTimes(t *testing.T) {
	r, _, routes := automaticFixture()
	now := time.Now()
	r.entries = map[string]*engineEntry{"a": {route: routes[0]}, "b": {route: routes[1]}}
	for _, v := range routes {
		v.TrafficDownBPS.Store(5_000_000)
		v.TrafficUpBPS.Store(2_000_000)
		v.TrafficAt.Store(now.Unix())
	}
	old := &Route{Config: routes[0].Config}
	old.TrafficAt.Store(now.Unix())
	old.TrafficDownBPS.Store(1_000_000)
	old.TrafficUpBPS.Store(500_000)
	r.entries["old-generation"] = &engineEntry{route: old}
	down, up := r.ownISPTraffic("a", now, now)
	if down != 6_000_000 || up != 2_500_000 {
		t.Fatal("ISP counters mixed", down, up)
	}
	for _, at := range []time.Time{now.Add(-10 * time.Second), now.Add(10 * time.Second)} {
		if down, up := r.ownISPTraffic("a", at, now); down != 0 || up != 0 {
			t.Fatal("temporally unrelated samples accepted")
		}
	}
}

func TestEqual800MbpsISPsUseEqualPriorWithOrWithoutTelemetry(t *testing.T) {
	r, p, routes := automaticFixture()
	for i := range r.config.EgressPaths {
		r.config.EgressPaths[i].DownloadMbps = 800
		r.config.EgressPaths[i].UploadMbps = 800
	}
	for _, telemetry := range []bool{false, true} {
		if telemetry {
			_, refresh := attachTelemetry(t, r)
			refresh()
		}
		p.egressCredit = nil
		got := allocate(r, p, routes, 1000)
		if math.Abs(float64(got["a"]-got["b"])) > 2 {
			t.Fatal("equal ISP capacity is not balanced", got)
		}
	}
}
