package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"proxyplatform/internal/config"
	"proxyplatform/internal/routertelemetry"
	"proxyplatform/internal/topology"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func automaticFixture() (*Runtime, *policyState, []*Route) {
	r := &Runtime{config: config.Config{EgressPaths: []config.EgressPath{{ID: "a", Enabled: true, Weight: 1, DownloadMbps: 100, UploadMbps: 100}, {ID: "b", Enabled: true, Weight: 999, DownloadMbps: 100, UploadMbps: 100}}}}
	p := newPolicyState(config.Policy{ID: "p", Mode: "auto", ActivePaths: 16})
	routes := []*Route{{Config: config.Route{ID: "a1", EgressID: "a", ServerHost: "192.0.2.1"}}, {Config: config.Route{ID: "b1", EgressID: "b", ServerHost: "192.0.2.2"}}}
	for _, v := range routes {
		v.RTTUS.Store(10000)
	}
	return r, p, routes
}

func allocate(r *Runtime, p *policyState, routes []*Route, n int) map[string]int {
	out := map[string]int{}
	for i := 0; i < n; i++ {
		chosen := r.selectEgressLocked(p, routes, nil)
		if len(chosen) > 0 {
			out[chosen[0].Config.EgressID]++
		}
	}
	return out
}

func attachTelemetry(t *testing.T, r *Runtime) (func(float64, float64, bool, time.Time), func()) {
	t.Helper()
	var value atomic.Value
	first, second := "a", "b"
	if len(r.config.EgressPaths) >= 2 {
		first = r.config.EgressPaths[0].ID
		second = r.config.EgressPaths[1].ID
	}
	set := func(rx, tx float64, down bool, at time.Time) {
		wans := []routertelemetry.WAN{{ID: "wan-a", ISP: first, Interface: "wan-a", Running: !down, Disabled: down, Quality: "measured", DownloadBPS: &rx, UploadBPS: &tx}, {ID: "wan-b", ISP: second, Interface: "wan-b", Running: true, Quality: "measured"}}
		zero := 0.0
		wans[1].DownloadBPS = &zero
		wans[1].UploadBPS = &zero
		if down {
			wans[0].Quality = "link_down"
		}
		if rx < 0 {
			wans[0].Quality = "counter_reset"
			wans[0].DownloadBPS = nil
			wans[0].UploadBPS = nil
		}
		d := routertelemetry.Document{Version: 1, Enabled: true, Mode: "observe", Routers: []routertelemetry.Snapshot{{RouterID: "home", State: "ready", Fresh: true, ObservedAt: at, Capabilities: []string{"wan.counters", "wan.link_state"}, Reading: routertelemetry.Reading{WANs: wans}}}}
		b, _ := json.Marshal(d)
		value.Store(b)
	}
	set(0, 0, false, time.Now())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.Write(value.Load().([]byte)) }))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(path, []byte(strings.Repeat("fixture", 8)), 0600)
	c, e := routertelemetry.NewConsumer(server.URL+"/v1/snapshot", path)
	if e != nil {
		t.Fatal(e)
	}
	r.mu.Lock()
	r.routerTelemetry = c
	r.mu.Unlock()
	refresh := func() { c.Refresh(context.Background()) }
	refresh()
	return set, refresh
}

func TestAutomaticISPWeightsWithoutRouterIgnoreManualWeights(t *testing.T) {
	r, p, routes := automaticFixture()
	got := allocate(r, p, routes, 1000)
	if math.Abs(float64(got["a"]-got["b"])) > 2 {
		t.Fatal("manual weights affected automatic default", got)
	}
	r.config.EgressPaths[0].DownloadMbps = 200
	p.egressCredit = nil
	got = allocate(r, p, routes, 900)
	if got["a"] < 590 || got["a"] > 610 {
		t.Fatal("capacity prior not automatic", got)
	}
}

func TestTelemetryCongestionStaleAndHardDisabledPath(t *testing.T) {
	r, p, routes := automaticFixture()
	set, refresh := attachTelemetry(t, r)
	set(11875000, 0, false, time.Now())
	refresh()
	got := allocate(r, p, routes, 1000)
	if got["a"] > 120 || got["a"] < 70 {
		t.Fatal("WAN congestion not reflected", got)
	}
	set(0, 11875000, false, time.Now())
	refresh()
	p.egressCredit = nil
	got = allocate(r, p, routes, 1000)
	if got["a"] > 120 {
		t.Fatal("upload congestion ignored", got)
	}
	set(0, 0, false, time.Now().Add(-time.Minute))
	refresh()
	p.egressCredit = nil
	got = allocate(r, p, routes, 1000)
	if got["a"] < 490 || got["a"] > 510 {
		t.Fatal("stale telemetry blocked autonomous routing", got)
	}
	set(0, 0, true, time.Now())
	refresh()
	if got = allocate(r, p, routes, 100); got["a"] != 0 || got["b"] != 100 {
		t.Fatal("down WAN selected", got)
	}
	r.config.EgressPaths[0].Enabled = false
	set(0, 0, false, time.Now())
	refresh()
	for i := 0; i < 100; i++ {
		for _, v := range r.selectEgressLocked(p, routes, nil) {
			if v.Config.EgressID == "a" {
				t.Fatal("disabled path in retry list")
			}
		}
	}
}

func TestAutomaticProfileWeightsShareIPCapacity(t *testing.T) {
	r, p, _ := automaticFixture()
	fast := &Route{Config: config.Route{ID: "fast", ServerHost: "192.0.2.1"}}
	slow := &Route{Config: config.Route{ID: "slow", ServerHost: "192.0.2.2"}}
	for _, v := range []*Route{fast, slow} {
		v.RTTUS.Store(10000)
		v.LastSpeed.Store(time.Now().Unix())
	}
	fast.SpeedBPS.Store(8000000)
	slow.SpeedBPS.Store(2000000)
	routes := []*Route{fast, slow}
	counts := map[string]int{}
	for i := 0; i < 1000; i++ {
		v := r.selectAutomaticProfileLocked(p, routes, nil)
		counts[v.Config.ServerHost]++
	}
	if counts["192.0.2.1"] < 790 || counts["192.0.2.1"] > 810 {
		t.Fatal("measured profile weights wrong", counts)
	}
	for i := 0; i < 8; i++ {
		v := &Route{Config: config.Route{ID: fmt.Sprint("alias", i), ServerHost: "192.0.2.1"}}
		v.RTTUS.Store(10000)
		v.LastSpeed.Store(time.Now().Unix())
		v.SpeedBPS.Store(8000000)
		routes = append(routes, v)
	}
	p.profileCredit = nil
	counts = map[string]int{}
	for i := 0; i < 1000; i++ {
		d := &topology.Decision{}
		v := r.selectAutomaticProfileLocked(p, routes, d)
		counts[v.Config.ServerHost]++
		if len(d.Candidates) != 2 || d.Candidates[0].ProfileWeight <= 0 || d.Candidates[0].ProfileWeight > 1 {
			t.Fatal("missing actual weight evidence")
		}
	}
	if counts["192.0.2.1"] < 790 || counts["192.0.2.1"] > 810 {
		t.Fatal("aliases multiplied shared capacity", counts)
	}
	remote := &Route{Config: config.Route{ID: "other-isp", EgressID: "other", ServerHost: "192.0.2.1"}}
	remote.Active.Store(20)
	r.entries = map[string]*engineEntry{"other": {route: remote}}
	p.profileCredit = nil
	counts = map[string]int{}
	for i := 0; i < 1000; i++ {
		counts[r.selectAutomaticProfileLocked(p, routes, nil).Config.ServerHost]++
	}
	if counts["192.0.2.2"] < 800 {
		t.Fatal("shared balancer active load ignored", counts)
	}
}

func TestPassiveImprovementKeepsFreshMeasuredCapacityBaseline(t *testing.T) {
	r, p, _ := automaticFixture()
	fast := &Route{Config: config.Route{ID: "fast", ServerHost: "192.0.2.1"}}
	slow := &Route{Config: config.Route{ID: "slow", ServerHost: "192.0.2.2"}}
	for _, v := range []*Route{fast, slow} {
		v.RTTUS.Store(10000)
		v.LastSpeed.Store(time.Now().Unix())
	}
	fast.SpeedBPS.Store(25_000_000)
	slow.SpeedBPS.Store(2_500_000)
	slow.PassiveBPS.Store(2_750_000)
	slow.LastPassive.Store(time.Now().Unix())
	fastCount := 0
	for i := 0; i < 1000; i++ {
		if r.selectAutomaticProfileLocked(p, []*Route{fast, slow}, nil) == fast {
			fastCount++
		}
	}
	if fastCount < 880 || fastCount > 930 {
		t.Fatal("slight passive improvement replaced measured slow capacity with unknown-path prior", fastCount)
	}
	// Without an active upper-capacity observation, low delivered goodput
	// must still not downgrade an unknown/idle path.
	slow.LastSpeed.Store(time.Now().Add(-time.Hour).Unix())
	p.profileCredit = nil
	fastCount = 0
	for i := 0; i < 1000; i++ {
		if r.selectAutomaticProfileLocked(p, []*Route{fast, slow}, nil) == fast {
			fastCount++
		}
	}
	if fastCount < 650 || fastCount > 690 {
		t.Fatal("passive lower bound was treated as an upper capacity", fastCount)
	}
}

func TestTelemetryFactorSlewLimit(t *testing.T) {
	r, p, routes := automaticFixture()
	set, refresh := attachTelemetry(t, r)
	set(1250000, 0, false, time.Now())
	refresh()
	first := r.automaticISPWeight(p, r.config.EgressPaths[0], routes[:1], 0, time.Now())
	set(11875000, 0, false, time.Now())
	refresh()
	second := r.automaticISPWeight(p, r.config.EgressPaths[0], routes[:1], 0, time.Now())
	if second.Factor < first.Factor*.75-1e-6 || second.Factor >= first.Factor {
		t.Fatal("unsmoothed step", first, second)
	}
}

func TestSingleCandidateHonorsDisabledISP(t *testing.T) {
	r := selectionFixture("auto")
	r.config.EgressPaths = []config.EgressPath{{ID: "a", Enabled: false}}
	r.current["a"].route.Config.EgressID = "a"
	r.policies["p"].best = []*Route{r.current["a"].route}
	d := &topology.Decision{}
	if len(r.candidatesObserved("policy:p", d)) != 0 {
		t.Fatal("single candidate bypassed ISP disable")
	}
	if len(d.Candidates) == 0 || d.Candidates[0].Reason != "isp_disabled" {
		t.Fatal("disabled ISP cause missing")
	}
}

func BenchmarkAutomaticSelection(b *testing.B) {
	r, p, routes := automaticFixture()
	for i := 2; i < 16; i++ {
		v := &Route{Config: config.Route{ID: fmt.Sprint("route", i), EgressID: []string{"a", "b"}[i%2], ServerHost: fmt.Sprint("192.0.2.", i+1)}}
		v.RTTUS.Store(20000)
		routes = append(routes, v)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.selectEgressLocked(p, routes, nil)
	}
}

func TestPassiveLowerBoundDoesNotBecomeCapacityCeiling(t *testing.T) {
	r, p, _ := automaticFixture()
	quiet := &Route{Config: config.Route{ID: "quiet", ServerHost: "192.0.2.1"}}
	idle := &Route{Config: config.Route{ID: "idle", ServerHost: "192.0.2.2"}}
	quiet.LastPassive.Store(time.Now().Unix())
	quiet.PassiveBPS.Store(1000)
	for _, v := range []*Route{quiet, idle} {
		v.RTTUS.Store(10000)
		v.CeilingBPS = 12500000
	}
	counts := map[string]int{}
	for i := 0; i < 1000; i++ {
		d := &topology.Decision{}
		v := r.selectAutomaticProfileLocked(p, []*Route{quiet, idle}, d)
		counts[v.Config.ID]++
		for _, c := range d.Candidates {
			if c.SpeedSource != "capacity_prior" || c.SpeedUsedBPS != 12500000 {
				t.Fatal("unknown capacity presented as passive ceiling", c)
			}
		}
	}
	if counts["quiet"] < 490 || counts["quiet"] > 510 {
		t.Fatal("small passive traffic poisoned profile weight", counts)
	}
}

func TestCounterResetClearsCongestionSmoothing(t *testing.T) {
	r, p, routes := automaticFixture()
	set, refresh := attachTelemetry(t, r)
	set(12000000, 0, false, time.Now())
	refresh()
	busy := r.automaticISPWeight(p, r.config.EgressPaths[0], routes[:1], 0, time.Now())
	if busy.Factor > .11 {
		t.Fatal("busy fixture")
	}
	set(-1, 0, false, time.Now())
	refresh()
	reset := r.automaticISPWeight(p, r.config.EgressPaths[0], routes[:1], 0, time.Now())
	if reset.Factor != 1 || reset.Utilization != nil {
		t.Fatal("reset retained congestion")
	}
	set(0, 0, false, time.Now())
	refresh()
	recovered := r.automaticISPWeight(p, r.config.EgressPaths[0], routes[:1], 0, time.Now())
	if recovered.Factor != 1 {
		t.Fatal("old pre-reset smoothing reappeared")
	}
}
