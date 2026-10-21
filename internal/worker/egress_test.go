package worker

import (
	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/topology"
	"testing"
	"time"
)

func TestEgressWeightsDoNotDependOnProfileCount(t *testing.T) {
	r := &Runtime{config: config.Config{EgressPaths: []config.EgressPath{{ID: "a", Enabled: true, Weight: 8}, {ID: "b", Enabled: true, Weight: 5}}}}
	p := &policyState{config: config.Policy{Mode: "auto", ActivePaths: 16}}
	routes := []*Route{}
	for i := 0; i < 10; i++ {
		id := "a"
		if i == 9 {
			id = "b"
		}
		routes = append(routes, &Route{Config: config.Route{ID: string(rune('a' + i)), EgressID: id}})
	}
	counts := map[string]int{}
	for i := 0; i < 1300; i++ {
		d := &topology.Decision{}
		selected := r.selectEgressLocked(p, routes, d)
		counts[selected[0].Config.EgressID]++
		if len(selected) != 10 || d.Method != "isp_weighted_path_choice" {
			t.Fatal("lost retries or evidence")
		}
	}
	if counts["a"] != 800 || counts["b"] != 500 {
		t.Fatal("profile aliases changed lane shares", counts)
	}
	// A failed/unavailable ISP cannot receive new allocations.
	for i := 0; i < 100; i++ {
		if r.selectEgressLocked(p, routes[:9], nil)[0].Config.EgressID != "a" {
			t.Fatal("selected unavailable ISP")
		}
	}
	// Return from outage does not produce unbounded catch-up credits.
	counts = map[string]int{}
	for i := 0; i < 130; i++ {
		counts[r.selectEgressLocked(p, routes, nil)[0].Config.EgressID]++
	}
	if counts["b"] < 45 || counts["b"] > 55 {
		t.Fatal("recovery burst", counts)
	}
}

func TestEgressUsesProfileGuardAndSourceSensitiveRestriction(t *testing.T) {
	v := config.Route{ID: "path_a", ProfileID: "profile", EgressID: "isp2", Fingerprint: "credential", Outbound: []byte(`{"protocol":"socks","sendThrough":"127.0.0.2"}`)}
	r := &Runtime{liveGuard: &config.LiveGuard{Routes: map[string]config.RoutePermit{"profile": {Fingerprint: "credential", ValidUntil: time.Now().Add(time.Hour)}}}}
	if !r.permitted("", v) {
		t.Fatal("profile guard rejects its path")
	}
	r.Restrict(config.Config{Routes: []config.Route{v}, Policies: []config.Policy{{ID: "p", Routes: []string{v.ID}}}})
	changed := v
	changed.Outbound = []byte(`{"protocol":"socks","sendThrough":"127.0.0.3"}`)
	if r.permitted("p", changed) {
		t.Fatal("old source permitted after source change")
	}
	delete(r.liveGuard.Routes, "profile")
	if r.permitted("p", v) {
		t.Fatal("revoked profile remains usable via ISP alias")
	}
}

func TestEgressHotWindowIncludesBothISPs(t *testing.T) {
	r := &Runtime{config: config.Config{EgressPaths: []config.EgressPath{{ID: "a"}, {ID: "b"}}}, current: map[string]*engineEntry{}, valid: map[string]time.Time{}, policies: map[string]*policyState{}}
	ids := []string{"a1", "a2", "b1"}
	p := newPolicyState(config.Policy{ID: "p", Mode: "auto", Routes: ids, ActivePaths: 2})
	r.policies["p"] = p
	for _, id := range ids {
		instance := &core.Instance{}
		instance.Ready.Store(true)
		v := &Route{Config: config.Route{ID: id, EgressID: id[:1], ServerHost: "127.0.0.1"}, Instance: instance}
		if id[0] == 'b' {
			v.RTTUS.Store(900000)
		} else {
			v.RTTUS.Store(1000)
		}
		r.current[id] = &engineEntry{route: v}
	}
	r.rebuildLocked()
	if len(p.best) != 2 || p.best[0].Config.EgressID == p.best[1].Config.EgressID {
		t.Fatal("one ISP suppressed all others")
	}
}
