package worker

import (
	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"testing"
	"time"
)

func TestAutomaticWeightsLearnWithoutConfiguredISPWeights(t *testing.T) {
	r := &Runtime{}
	p := &policyState{config: config.Policy{Mode: "auto", ActivePaths: 2}}
	a, b := &Route{Config: config.Route{ID: "a", ServerHost: "1.1.1.1"}, Instance: &core.Instance{}}, &Route{Config: config.Route{ID: "b", ServerHost: "8.8.8.8"}, Instance: &core.Instance{}}
	a.SpeedBPS.Store(16 << 20)
	b.SpeedBPS.Store(1 << 20)
	a.LastSpeed.Store(time.Now().Unix())
	b.LastSpeed.Store(time.Now().Unix())
	a.RTTUS.Store(20000)
	b.RTTUS.Store(20000)
	weights := r.automaticWeights(p, []*Route{a, b}, time.Now())
	if weights[a] <= weights[b]*2 || weights[b] <= 0 {
		t.Fatal("capacity not learned or exploration starved", weights)
	}
	a.ErrorEWMA.Store(1000)
	weights = r.automaticWeights(&policyState{config: p.config}, []*Route{a, b}, time.Now())
	if weights[a] >= weights[b] {
		t.Fatal("failure penalty ignored")
	}
}

func TestIdleConnectionsAreNotBulkCongestion(t *testing.T) {
	r := &Runtime{}
	now := time.Now()
	a, b := readyRoute("a"), readyRoute("b")
	a.Config.ServerHost = "1.1.1.1"
	b.Config.ServerHost = "8.8.8.8"
	a.Active.Store(100)
	b.Active.Store(1)
	p := &policyState{config: config.Policy{Mode: "auto"}}
	w := r.automaticWeights(p, []*Route{a, b}, now)
	if w[a] != w[b] {
		t.Fatal("idle sockets treated as saturated traffic", w[a], w[b])
	}
	a.TrafficAt.Store(now.Unix())
	b.TrafficAt.Store(now.Unix())
	a.SpeedBPS.Store(1 << 20)
	a.LastSpeed.Store(now.Unix())
	a.TrafficDownBPS.Store(1 << 20)
	b.SpeedBPS.Store(1 << 20)
	b.LastSpeed.Store(now.Unix())
	w = r.automaticWeights(&policyState{config: p.config}, []*Route{a, b}, now)
	if w[a] >= w[b] {
		t.Fatal("actual saturation ignored")
	}
}

func TestAutomaticCapacityRatioAndBoundedGrowth(t *testing.T) {
	r := &Runtime{}
	now := time.Now()
	a, b := readyRoute("a"), readyRoute("b")
	a.Config.ServerHost, b.Config.ServerHost = "1.1.1.1", "8.8.8.8"
	a.SpeedBPS.Store(16 << 20)
	b.SpeedBPS.Store(1 << 20)
	a.LastSpeed.Store(now.Unix())
	b.LastSpeed.Store(now.Unix())
	p := &policyState{config: config.Policy{Mode: "throughput"}}
	w := r.automaticWeights(p, []*Route{a, b}, now)
	if w[a] < .9 {
		t.Fatalf("measured capacity diluted: %v", w[a])
	}
	a.SpeedBPS.Store(1 << 20)
	b.SpeedBPS.Store(16 << 20)
	before := w[b]
	for i := 1; i <= 6; i++ {
		w = r.automaticWeights(p, []*Route{a, b}, now.Add(time.Duration(i)*5*time.Second))
	}
	if w[b]-before > .100001 {
		t.Fatalf("allocation grew more than ten percentage points in 30s: %v", w[b]-before)
	}
}
