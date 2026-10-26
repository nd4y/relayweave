package worker

import (
	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"testing"
	"time"
)

func discoveryFixture() (*Runtime, *policyState) {
	p := newPolicyState(config.Policy{ID: "p", Mode: "throughput", ActivePaths: 1, Routes: []string{"hot", "reserve"}})
	r := &Runtime{current: map[string]*engineEntry{}, policies: map[string]*policyState{"p": p}, valid: map[string]time.Time{}}
	r.RenewLease(time.Now().Add(time.Hour))
	for _, id := range p.config.Routes {
		instance := &core.Instance{}
		instance.Ready.Store(true)
		r.current[id] = &engineEntry{route: &Route{Config: config.Route{ID: id}, Instance: instance}}
	}
	p.best = []*Route{r.current["hot"].route}
	return r, p
}

func TestCapacityDiscoveryIncludesReserveWithoutChangingClientSelection(t *testing.T) {
	r, p := discoveryFixture()
	p.egressCredit = map[string]float64{"a": .25}
	r.cursor.Store(42)
	for i := 0; i < 10; i++ {
		got := r.capacityProbeCandidates("p")
		if len(got) != 2 || got[0].Config.ID != "hot" || got[1].Config.ID != "reserve" {
			t.Fatal("unmeasured reserve cannot participate", got)
		}
	}
	if r.cursor.Load() != 42 || p.egressCredit["a"] != .25 || len(p.best) != 1 || p.best[0].Config.ID != "hot" || len(r.current) != 2 {
		t.Fatal("discovery changed client allocation or engine membership")
	}
}

func TestCapacityDiscoveryPreservesAdmissionGates(t *testing.T) {
	tests := map[string]func(*Runtime, *policyState){
		"staged":    func(r *Runtime, _ *policyState) { r.current["reserve"].staged = true },
		"not ready": func(r *Runtime, _ *policyState) { r.current["reserve"].route.Instance.Ready.Store(false) },
		"circuit open": func(r *Runtime, _ *policyState) {
			r.current["reserve"].route.BlockedUntil.Store(time.Now().Add(time.Minute).UnixNano())
		},
		"expired":       func(r *Runtime, _ *policyState) { r.valid["reserve"] = time.Now().Add(-time.Second) },
		"not in policy": func(_ *Runtime, p *policyState) { delete(p.rank, "reserve") },
		"revoked": func(r *Runtime, _ *policyState) {
			r.allowed = map[string]map[string]string{"p": {"hot": fingerprint(r.current["hot"].route.Config)}}
		},
		"established only": func(r *Runtime, _ *policyState) { r.current["reserve"].route.Config.EstablishedOnly = true },
		"ISP disabled": func(r *Runtime, _ *policyState) {
			r.config.EgressPaths = []config.EgressPath{{ID: "on", Enabled: true}, {ID: "off"}}
			r.current["hot"].route.Config.EgressID = "on"
			r.current["reserve"].route.Config.EgressID = "off"
		},
		"target unverified": func(r *Runtime, p *policyState) {
			p.config.ProbeScope, p.config.ProbeURL = "target", "https://example.test/"
			r.targetHealth = map[string]targetObservation{"p/hot": {Signature: targetSignature(p.config), Checked: time.Now(), Healthy: true}}
		},
	}
	for _, state := range []string{"warming", "canary", "paused", "rolled_back"} {
		tests[state] = func(r *Runtime, _ *policyState) { r.rollovers = map[string]*pathRollover{"reserve": {state: state}} }
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r, p := discoveryFixture()
			mutate(r, p)
			got := r.capacityProbeCandidates("p")
			if len(got) != 1 || got[0].Config.ID != "hot" {
				t.Fatal("ineligible engine received a capacity probe", got)
			}
		})
	}
	for _, state := range []string{"active_draining", "completed"} {
		r, _ := discoveryFixture()
		r.rollovers = map[string]*pathRollover{"reserve": {state: state}}
		if len(r.capacityProbeCandidates("p")) != 2 {
			t.Fatal("promoted engine excluded", state)
		}
	}
	r, _ := discoveryFixture()
	r.drained.Store(true)
	if len(r.capacityProbeCandidates("p")) != 0 {
		t.Fatal("drained runtime allowed probes")
	}
	r.drained.Store(false)
	r.RenewLease(time.Now().Add(-time.Minute))
	if len(r.capacityProbeCandidates("p")) != 0 {
		t.Fatal("expired lease allowed probes")
	}
}
