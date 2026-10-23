package worker

import (
	"proxyplatform/internal/topology"
	"testing"
	"time"
)

func TestFreshAggregateDeliveryImprovesCapacityWithoutDowngradingActiveSample(t *testing.T) {
	now := time.Now()
	r := &Route{}
	r.RTTUS.Store(10000)
	r.SpeedBPS.Store(10_000_000)
	r.LastSpeed.Store(now.Unix())
	r.TrafficDownBPS.Store(25_000_000)
	r.TrafficAt.Store(now.Unix())
	var e topology.Candidate
	r.scoreObserved("auto", &e)
	if e.SpeedUsedBPS != 25_000_000 || e.SpeedBPS != 10_000_000 || e.SpeedSource != "delivered_lower_bound" {
		t.Fatal("aggregate delivery did not improve the short active sample", e)
	}
	r.TrafficDownBPS.Store(1_000_000)
	r.scoreObserved("auto", &e)
	if e.SpeedUsedBPS != 10_000_000 || r.SpeedBPS.Load() != 10_000_000 {
		t.Fatal("lower delivered rate downgraded independent capacity", e)
	}
	r.TrafficDownBPS.Store(25_000_000)
	r.TrafficAt.Store(now.Add(-time.Minute).Unix())
	r.scoreObserved("auto", &e)
	if e.SpeedUsedBPS != 10_000_000 {
		t.Fatal("stale aggregate was used", e)
	}
}

func TestAggregateDropOnlyRequestsBoundedConfirmation(t *testing.T) {
	now := time.Now()
	r := &Route{}
	r.SpeedBPS.Store(25_000_000)
	r.LastSpeed.Store(now.Add(-time.Minute).Unix())
	r.Active.Store(4)
	r.requestOnAggregateDrop(25_000_000, 2_500_000, now)
	if r.ProbeRequestedAt.Load() == 0 || r.SpeedBPS.Load() != 25_000_000 {
		t.Fatal("capacity drop was not confirmed independently")
	}
	r.ProbeRequestedAt.Store(0)
	r.ProbeNotBefore.Store(now.Add(time.Minute).Unix())
	r.requestOnAggregateDrop(25_000_000, 2_500_000, now)
	if r.ProbeRequestedAt.Load() != 0 {
		t.Fatal("aggregate signal bypassed cooldown")
	}
	r.ProbeNotBefore.Store(0)
	r.Active.Store(0)
	r.requestOnAggregateDrop(25_000_000, 2_500_000, now)
	if r.ProbeRequestedAt.Load() != 0 {
		t.Fatal("idle path spent measurement allowance")
	}
}

func TestCapacityRefreshSpendsOnlyForUnknownTrafficOrChange(t *testing.T) {
	now := time.Now()
	r := &Route{}
	if !r.capacityRefreshUseful(now) {
		t.Fatal("unknown route needs its initial sample")
	}
	r.LastSpeed.Store(now.Add(-10 * time.Minute).Unix())
	r.Connections.Store(12)
	r.CapacityConnections.Store(12)
	if r.capacityRefreshUseful(now) {
		t.Fatal("idle measured route must preserve the shared allowance")
	}
	r.Connections.Add(1)
	if !r.capacityRefreshUseful(now) {
		t.Fatal("new user traffic must resume periodic refresh")
	}
	r.CapacityConnections.Store(13)
	r.Active.Store(1)
	if !r.capacityRefreshUseful(now) {
		t.Fatal("an existing active transfer must remain eligible")
	}
	r.LastSpeed.Store(now.Unix())
	if r.capacityRefreshUseful(now) {
		t.Fatal("activity must not bypass the normal cadence")
	}
	r.requestCapacityProbe(now.Unix())
	if !r.capacityRefreshUseful(now) {
		t.Fatal("a detected change must override the periodic cadence")
	}
}
