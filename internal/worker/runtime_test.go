package worker

import (
	"context"
	"errors"
	"fmt"
	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/topology"
	"testing"
	"time"
)

func readyRoute(id string) *Route {
	i := &core.Instance{}
	i.Ready.Store(true)
	r := &Route{Config: config.Route{ID: id, CapacityGroup: id}, Instance: i}
	r.RTTUS.Store(10000)
	return r
}
func selectionFixture(mode string) *Runtime {
	r := &Runtime{ctx: context.Background(), entries: map[string]*engineEntry{}, current: map[string]*engineEntry{}, policies: map[string]*policyState{}, valid: map[string]time.Time{}, slots: map[int]bool{}}
	a, b := readyRoute("a"), readyRoute("b")
	p := &policyState{config: config.Policy{ID: "p", Mode: mode, Routes: []string{"a", "b"}, ActivePaths: 2, StickySeconds: 600, RotationSeconds: 30}, best: []*Route{a, b}, changed: time.Now()}
	r.current["a"] = &engineEntry{route: a}
	r.current["b"] = &engineEntry{route: b}
	r.policies["p"] = p
	r.RenewLease(time.Now().Add(time.Hour))
	return r
}
func TestSelectionHardConstraints(t *testing.T) {
	r := selectionFixture("round-robin")
	if len(r.Candidates("policy:p")) != 2 {
		t.Fatal("missing routes")
	}
	r.valid["a"] = time.Now().Add(-time.Second)
	paths := r.Candidates("policy:p")
	if len(paths) != 1 || paths[0].Config.ID != "b" {
		t.Fatal("TTL bypass")
	}
	r.drained.Store(true)
	if len(r.Candidates("policy:p")) != 0 {
		t.Fatal("drain bypass")
	}
	r.drained.Store(false)
	r.RenewLease(time.Now().Add(-time.Second))
	if len(r.Candidates("policy:p")) != 0 {
		t.Fatal("lease bypass")
	}
}
func TestRoundRobinAndManual(t *testing.T) {
	r := selectionFixture("round-robin")
	if r.Candidates("policy:p")[0].Config.ID == r.Candidates("policy:p")[0].Config.ID {
		t.Fatal("not rotating")
	}
	r.policies["p"].config.Mode = "manual"
	r.policies["p"].best[0].Instance.Ready.Store(false)
	if len(r.Candidates("policy:p")) != 0 {
		t.Fatal("manual silently fell back")
	}
}
func TestCircuitAndEWMA(t *testing.T) {
	r := readyRoute("x")
	for range 3 {
		r.failed()
	}
	if r.BlockedUntil.Load() < time.Now().UnixNano() {
		t.Fatal("circuit not open")
	}
	r.success(20 * time.Millisecond)
	if r.BlockedUntil.Load() != 0 || r.ConsecutiveFailures.Load() != 0 {
		t.Fatal("recovery not recorded")
	}
	if r.RTTUS.Load() != 11250 {
		t.Fatalf("EWMA %d", r.RTTUS.Load())
	}
	r.Active.Store(8)
	if r.score("latency") <= 11.25 {
		t.Fatal("load ignored")
	}
}
func TestStaleCapacityIsOnlyCautiousPriorAndEventuallyExpires(t *testing.T) {
	r := readyRoute("x")
	base := r.score("throughput")
	r.SpeedBPS.Store(100000000)
	r.LastSpeed.Store(time.Now().Add(-time.Hour).Unix())
	var e topology.Candidate
	historical := r.scoreObserved("throughput", &e)
	if e.SpeedBPS != 0 || e.SpeedUsedBPS != 12500000 || e.SpeedSource != "historical_capacity_prior" {
		t.Fatal("stale result presented as a fresh measurement", e)
	}
	r.LastSpeed.Store(time.Now().Add(-25 * time.Hour).Unix())
	if r.score("throughput") != base {
		t.Fatal("expired historical prior retained indefinitely")
	}
	r.LastSpeed.Store(time.Now().Unix())
	if r.score("throughput") >= historical {
		t.Fatal("fresh capacity ignored")
	}
}

func TestThroughputSelectionDoesNotMistakeFastPathQueueingForLowCapacity(t *testing.T) {
	fast, slow := readyRoute("fast"), readyRoute("slow")
	fast.SpeedBPS.Store(25_000_000)
	slow.SpeedBPS.Store(2_500_000)
	fast.LastSpeed.Store(time.Now().Unix())
	slow.LastSpeed.Store(time.Now().Unix())
	fast.Instance.LatencyUS.Store(100000)
	slow.Instance.LatencyUS.Store(1000)
	fast.Active.Store(3)
	if fast.score("throughput") >= slow.score("throughput") {
		t.Fatal("short queueing spike diverted fourth equal-size flow to 10x slower path")
	}
	fast.Active.Store(20)
	if fast.score("throughput") <= slow.score("throughput") {
		t.Fatal("load never permits use of second available capacity")
	}
}

func TestSelectionUsesNetworkProbeAndFreshDeliveredFloor(t *testing.T) {
	r := readyRoute("x")
	r.RTTUS.Store(1000)
	r.Instance.LatencyUS.Store(100000)
	if r.score("latency") < 100 {
		t.Fatal("loopback handshake hid real upstream latency")
	}
	base := r.score("auto")
	r.PassiveBPS.Store(100000000)
	r.LastPassive.Store(time.Now().Unix())
	if r.score("auto") >= base {
		t.Fatal("fresh delivered lower bound ignored")
	}
	r.LastPassive.Store(time.Now().Add(-time.Minute).Unix())
	if r.score("auto") != base {
		t.Fatal("stale passive delivery used as current evidence")
	}
}
func TestACL(t *testing.T) {
	g := New(context.Background(), config.Config{}, nil)
	l := config.Listener{AllowCIDRs: []string{"192.168.0.0/24", "fd00::/8"}}
	for _, addr := range []string{"192.168.0.2:1234", "[fd00::2]:1234"} {
		if _, ok := g.effective(l, addr); !ok {
			t.Fatal(addr)
		}
	}
	for _, addr := range []string{"192.168.1.2:1234", "[2001:db8::2]:1234", "invalid"} {
		if _, ok := g.effective(l, addr); ok {
			t.Fatal(addr)
		}
	}
}
func TestRevocationBeforeCanary(t *testing.T) {
	r := selectionFixture("round-robin")
	a := r.current["a"].route
	a.Config.Outbound = []byte(`{"id":"old"}`)
	b := r.current["b"].route
	b.Config.Outbound = []byte(`{"id":"b"}`)
	r.Restrict(config.Config{Routes: []config.Route{{ID: "a", Outbound: []byte(`{"id":"new"}`)}, {ID: "b", Outbound: b.Config.Outbound}}, Policies: []config.Policy{{ID: "p", Routes: []string{"a", "b"}}}})
	paths := r.Candidates("policy:p")
	if len(paths) != 1 || paths[0].Config.ID != "b" {
		t.Fatal("revoked credentials still receive connections")
	}
	r.Restrict(config.Config{})
	if len(r.Candidates("policy:p")) != 0 {
		t.Fatal("empty allowed set was ignored")
	}
}

func TestNewExpiryRestrictsOldGeneration(t *testing.T) {
	r := selectionFixture("round-robin")
	r.Restrict(config.Config{Routes: []config.Route{{ID: "a", ValidUntil: time.Now().Add(-time.Second)}, {ID: "b"}}, Policies: []config.Policy{{ID: "p", Routes: []string{"a", "b"}}}})
	paths := r.Candidates("policy:p")
	if len(paths) != 1 || paths[0].Config.ID != "b" {
		t.Fatal("new expiration bypassed by older running configuration")
	}
}

func TestWarmReserveNotInNormalRotation(t *testing.T) {
	r := selectionFixture("round-robin")
	r.policies["p"].config.ActivePaths = 1
	for range 10 {
		if r.Candidates("policy:p")[0].Config.ID != "a" {
			t.Fatal("reserve used before active path failed")
		}
	}
	r.current["a"].route.Instance.Ready.Store(false)
	if r.Candidates("policy:p")[0].Config.ID != "b" {
		t.Fatal("reserve not available on failure")
	}
}

func TestRemovedListenerHandlesAreReleased(t *testing.T) {
	g := New(context.Background(), config.Config{}, nil)
	defer g.Close()
	for i := range 20 {
		if e := g.Reconfigure(config.Config{Listeners: []config.Listener{{ID: fmt.Sprint(i), Listen: "127.0.0.1:0", Protocol: "http"}}}); e != nil {
			t.Fatal(e)
		}
		if e := g.Reconfigure(config.Config{}); e != nil {
			t.Fatal(e)
		}
		if len(g.bound) != 0 || len(g.servers) != 0 || len(g.listeners) != 0 || len(g.transports) != 0 {
			t.Fatal("removed HTTP listener retained")
		}
	}
}

func TestCancelledApplyDoesNotReplaceAcceptedListeners(t *testing.T) {
	r := selectionFixture("auto")
	r.config.Generation = 1
	g := New(context.Background(), config.Config{}, nil)
	defer g.Close()
	if err := g.Reconfigure(config.Config{Listeners: []config.Listener{{ID: "active", Protocol: "http", Listen: "127.0.0.1:0"}}}); err != nil {
		t.Fatal(err)
	}
	c := config.Config{Generation: 2, Managed: true, StateDir: t.TempDir(), MetricsListen: "127.0.0.1:0", ProbeURL: "http://example.invalid/health", UDP: config.UDP{Bind: "127.0.0.1", Advertise: "127.0.0.1", PortMin: 31000, PortMax: 31000}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Apply(ctx, c, g); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled candidate accepted: %v", err)
	}
	if r.Configuration().Generation != 1 || len(g.bound) != 1 {
		t.Fatal("cancelled candidate replaced the accepted configuration")
	}
}

func TestReenabledProcessMayProbeNewGeneration(t *testing.T) {
	r := selectionFixture("auto")
	v := r.current["a"].route.Config
	r.config.Generation = 4
	r.staging = map[string]probePermit{"a-old": {generation: 1}}
	delete(r.current, "a")
	if r.probeAllowed("a-old", v) {
		t.Fatal("retired instance probes without a staging generation")
	}
	r.staging["a-old"] = probePermit{generation: 5, until: time.Now().Add(time.Minute)}
	if !r.probeAllowed("a-old", v) {
		t.Fatal("re-enabled existing process cannot pass canary")
	}
	r.staging["a-old"] = probePermit{generation: 5, until: time.Now().Add(-time.Second)}
	if r.probeAllowed("a-old", v) {
		t.Fatal("staging expiry ignored")
	}
}

func TestRefreshedExpiryCanProbeWithoutRevivingBusinessTraffic(t *testing.T) {
	r := selectionFixture("auto")
	v := r.current["a"].route.Config
	r.current["a"].key = "a-current"
	r.config.Generation = 4
	r.valid["a"] = time.Now().Add(-time.Second)
	delete(r.current, "b")
	r.policies["p"].best = []*Route{r.current["a"].route}
	v.ValidUntil = time.Now().Add(time.Minute)
	c := config.Config{Generation: 5, Routes: []config.Route{v}, Policies: []config.Policy{{ID: "p", Routes: []string{"a"}}}}
	r.Restrict(c)
	r.staging = map[string]probePermit{"a-current": {generation: 4, until: v.ValidUntil}}
	if r.probeAllowed("a-current", v) {
		t.Fatal("stale staging permit revived an expired active route")
	}
	r.staging["a-current"] = probePermit{generation: 5, until: v.ValidUntil}
	if !r.probeAllowed("a-current", v) {
		t.Fatal("fresh subscription expiry cannot revalidate the existing process")
	}
	if len(r.Candidates("policy:p")) != 0 {
		t.Fatal("a canary permit revived expired business traffic before Apply")
	}
	r.Restrict(config.Config{Generation: 6})
	if r.probeAllowed("a-current", v) {
		t.Fatal("staging permit bypassed a newer profile revocation")
	}
	r.Restrict(c)
	r.staging["a-current"] = probePermit{generation: 5, until: time.Now().Add(-time.Second)}
	if r.probeAllowed("a-current", v) {
		t.Fatal("expired refreshed permit was accepted")
	}
	r.staging["a-current"] = probePermit{generation: 5, until: v.ValidUntil}
	r.config.Generation = 5
	r.valid["a"] = v.ValidUntil
	if !r.probeAllowed("a-current", v) || len(r.Candidates("policy:p")) != 1 {
		t.Fatal("accepted renewed configuration did not restore the route")
	}
}

func TestSharedProfileHealthUsesIndependentPolicyTargets(t *testing.T) {
	c := config.Config{Policies: []config.Policy{{Routes: []string{"a"}, ProbeURL: "https://one.example/", ProbeIntervalSeconds: 20}, {Routes: []string{"a"}, ProbeURL: "https://two.example/", ProbeFallbackURLs: []string{"https://one.example/"}, ProbeIntervalSeconds: 10}}}
	p := routeProbeConfig(c, "a")
	if p.ProbeURL != "https://one.example/" || len(p.ProbeFallbackURLs) != 1 || p.ProbeFallbackURLs[0] != "https://two.example/" || p.ProbeIntervalSeconds != 10 {
		t.Fatal(p)
	}
}
