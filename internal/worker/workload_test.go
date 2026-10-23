package worker

import (
	"proxyplatform/internal/topology"
	"testing"
	"time"
)

func TestWorkloadDiscountsOnlyFreshObservedIdleTCP(t *testing.T) {
	r := &Route{}
	now := time.Now()
	r.Active.Store(100)
	r.IdleTCP.Store(90)
	if got := r.workload(now); got != 100 {
		t.Fatal("unobserved sockets discounted", got)
	}
	r.WorkloadAt.Store(now.Unix())
	if got := r.workload(now); got != 14.5 {
		t.Fatal("busy/new/UDP work was lost", got)
	}
	if got := r.workload(now.Add(4 * time.Second)); got != 100 {
		t.Fatal("stale sampler discounted work", got)
	}
	r.Active.Store(2)
	if got := r.workload(now); got != .1 {
		t.Fatal("closed sockets made negative load", got)
	}
}

func TestHistoricalSlowCapacityDoesNotBecomeFasterUnknown(t *testing.T) {
	r := &Route{}
	r.SpeedBPS.Store(2_500_000)
	r.LastSpeed.Store(time.Now().Add(-20 * time.Minute).Unix())
	var e topology.Candidate
	r.scoreObserved("auto", &e)
	if e.SpeedUsedBPS != 2_500_000 || e.SpeedBPS != 0 || e.SpeedSource != "historical_capacity_prior" {
		t.Fatal(e)
	}
	r.TrafficAt.Store(time.Now().Unix())
	r.TrafficDownBPS.Store(25_000_000)
	r.scoreObserved("auto", &e)
	if e.SpeedUsedBPS != 25_000_000 {
		t.Fatal("new delivery cannot improve historical prior", e)
	}
}

func TestDeliveryWindowDirectionAndObserverGap(t *testing.T) {
	w := &deliveryWindow{}
	now := time.Now()
	w.observe(1000, 100, now)
	w.observe(2000, 200, now.Add(time.Second))
	if w.Down10.Load() <= 1000 || w.Down10.Load() >= 2000 || w.Up10.Load() >= w.Down10.Load() || w.Down60.Load() >= w.Down10.Load() {
		t.Fatal("invalid independent smoothed directions")
	}
	w.observe(0, 0, now.Add(time.Minute))
	if w.Down10.Load() != 0 || w.Down60.Load() != 0 {
		t.Fatal("observer stall retained apparently fresh history")
	}
}
