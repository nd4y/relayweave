package worker

import (
	"context"
	"encoding/json"
	"proxyplatform/internal/config"
	"testing"
	"time"
)

func TestEndpointDiversitySharedBalancerAcrossSources(t *testing.T) {
	now := time.Now()
	r := selectionFixture("throughput")
	a, b, c, d := readyRoute("a"), readyRoute("b"), readyRoute("c"), readyRoute("d")
	a.Config.ServerHost = "one.example"
	b.Config.ServerHost = "alias.example"
	c.Config.ServerHost = "other.example"
	d.Config.ServerHost = "overlap.example"
	a.Config.CapacityGroup = "provider-a/one.example"
	b.Config.CapacityGroup = "provider-b/alias.example"
	r.endpointIPs = map[string]endpointResolution{
		"one.example":     {IPs: []string{"192.0.2.1"}, Observed: now},
		"alias.example":   {IPs: []string{"192.0.2.1"}, Observed: now},
		"other.example":   {IPs: []string{"192.0.2.2"}, Observed: now},
		"overlap.example": {IPs: []string{"192.0.2.1", "2001:db8::1"}, Observed: now},
	}
	r.current["a"].route = a
	r.current["b"].route = b
	r.current["c"] = &engineEntry{route: c}
	r.current["d"] = &engineEntry{route: d}
	p := r.policies["p"]
	p.config.Routes = []string{"a", "b", "c", "d"}
	p.config.ActivePaths = 2
	p.config.WarmReserve = 2
	p.rank = map[string]int{"a": 0, "b": 1, "c": 2, "d": 3}
	r.rebuildLocked()
	if len(p.best) != 4 || p.best[0] != a || p.best[1] != c {
		t.Fatal("shared IP aliases crowded out an independent endpoint")
	}
	if p.best[2] != b || p.best[3] != d {
		t.Fatal("shared balancer alternatives discarded")
	}
	c.Instance.Ready.Store(false)
	r.rebuildLocked()
	if len(p.best) != 3 {
		t.Fatal("shared IP routes must remain available for fallback")
	}
}

func TestEndpointGroupsTransitiveAndUnresolved(t *testing.T) {
	now := time.Now()
	r := selectionFixture("auto")
	routes := []*Route{readyRoute("a"), readyRoute("b"), readyRoute("c"), readyRoute("d"), readyRoute("e")}
	for i, v := range routes {
		v.Config.ServerHost = []string{"a.example", "b.example", "c.example", "same.example", "same.example"}[i]
	}
	r.endpointIPs = map[string]endpointResolution{
		"a.example": {IPs: []string{"192.0.2.1"}, Observed: now},
		"b.example": {IPs: []string{"192.0.2.1", "192.0.2.2"}, Observed: now},
		"c.example": {IPs: []string{"192.0.2.2"}, Observed: now},
	}
	groups := r.endpointGroupsLocked(routes, now)
	if groups[0] != groups[1] || groups[1] != groups[2] || groups[3] != groups[4] || groups[0] == groups[3] {
		t.Fatal("overlapping DNS sets or same unresolved hostname not grouped")
	}
}

func TestEndpointCacheFailureExpiryAndDNSChange(t *testing.T) {
	now := time.Now()
	r := selectionFixture("auto")
	a, b := r.current["a"].route, r.current["b"].route
	a.Config.ServerHost = "a.example"
	b.Config.ServerHost = "b.example"
	r.endpointIPs = map[string]endpointResolution{
		"a.example": {IPs: []string{"192.0.2.1"}, Observed: now},
		"b.example": {IPs: []string{"192.0.2.1"}, Observed: now},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.refreshEndpointIPs(ctx)
	if len(r.routeIPsLocked(a, now)) != 1 {
		t.Fatal("temporary DNS failure lost last-good IP")
	}
	if len(r.routeIPsLocked(a, now.Add(6*time.Minute))) != 0 {
		t.Fatal("stale DNS retained indefinitely")
	}
	r.endpointIPs["b.example"] = endpointResolution{IPs: []string{"192.0.2.2"}, Observed: now}
	g := r.endpointGroupsLocked([]*Route{a, b}, now)
	if g[0] == g[1] {
		t.Fatal("DNS change did not split endpoints")
	}
}

func TestEndpointLiteralNormalizationAndLegacy(t *testing.T) {
	r := selectionFixture("auto")
	a, b := r.current["a"].route, r.current["b"].route
	a.Config.ServerHost = "::ffff:192.0.2.1"
	b.Config.ServerHost = "192.0.2.1"
	g := r.endpointGroupsLocked([]*Route{a, b}, time.Now())
	if g[0] != g[1] {
		t.Fatal("mapped IPv4 alias not normalized")
	}
	a.Config = config.Route{ID: "a", CapacityGroup: "old-source/EXAMPLE.COM."}
	if endpointHost(a) != "example.com" {
		t.Fatal("persisted legacy route lost hostname")
	}
	a.Config.ServerHost = "192.0.2.1"
	b.Config.ServerHost = "192.0.2.2"
	r.refreshEndpointIPs(context.Background())
	if len(r.endpointIPs["192.0.2.2"].IPs) != 1 {
		t.Fatal("literal IP refresh failed")
	}
	a.Config = config.Route{ID: "a", Outbound: json.RawMessage(`{"settings":{"vnext":[{"address":"WARMED.EXAMPLE."}]}}`)}
	if endpointHost(a) != "warmed.example" {
		t.Fatal("manually warmed core lost endpoint identity")
	}
	a.Config = config.Route{ID: "a", Outbound: json.RawMessage(`{"settings":{"servers":[{"address":"192.0.2.9"}]}}`)}
	if endpointHost(a) != "192.0.2.9" {
		t.Fatal("server-based outbound lost endpoint identity")
	}
}
