package worker

import (
	"testing"
	"time"
)

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
