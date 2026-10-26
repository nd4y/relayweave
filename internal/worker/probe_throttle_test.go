package worker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/wire"
)

func TestThrottledCapacityOriginDoesNotFailRouteOrSpendAgainDuringCooldown(t *testing.T) {
	if _, err := core.AdapterFor("worker-probe-budget-test"); err != nil {
		if err := core.RegisterAdapter(probeBudgetFixture{}); err != nil {
			t.Fatal(err)
		}
	}
	a := &Agent{dir: t.TempDir(), ctx: context.Background()}
	if err := a.load(); err != nil {
		t.Fatal(err)
	}
	r := classifiedBudgetFixture()
	a.runtime = r
	r.lease.Store(time.Now().Add(time.Hour).Unix())
	var requests atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer origin.Close()
	private, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	c := config.Config{Cores: []config.Core{{ID: "x", Version: "test"}}, Routes: []config.Route{{ID: "route", Core: "x", Endpoint: private.Addr().String(), Outbound: json.RawMessage(`{"protocol":"freedom"}`)}}, Policies: []config.Policy{{ID: "p", Routes: []string{"route"}, SpeedURL: origin.URL, ProbeBudgetBytes: 128 << 20}}}
	r.config = c
	key := routeIdentity(c.Routes[0], "test/")
	e := &engineEntry{key: key, route: &Route{Config: c.Routes[0], Instance: &core.Instance{Config: config.Core{Family: "worker-probe-budget-test"}}}}
	r.entries = map[string]*engineEntry{key: e}
	e.route.SpeedBPS.Store(25_000_000)
	payload, _ := json.Marshal(profileProbe{ProbeURL: origin.URL, SpeedURL: origin.URL, PolicyID: "p", Budget: 128 << 20, Automatic: true})
	command := wire.Command{Action: "speed-test", Target: "route", Payload: payload}
	if _, err := a.test(context.Background(), command); err == nil || err.Error() != "probe_target_throttled" {
		t.Fatal(err)
	}
	if _, err := a.test(context.Background(), command); err == nil || err.Error() != "probe_target_cooldown" {
		t.Fatal(err)
	}
	if requests.Load() != 1 || r.budget["p"] != automaticCapacityProbeBytes || e.route.SpeedBPS.Load() != 25_000_000 || e.route.Failures.Load() != 0 {
		t.Fatal("origin throttling changed route capacity/health or spent another reservation")
	}
	if !e.route.probeTargetCooling(time.Now()) || e.route.ProbeHTTPStatus.Load() != 429 {
		t.Fatal("cooldown not visible")
	}
}

func TestProbeRetryAfterBounds(t *testing.T) {
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		header string
		delay  time.Duration
	}{
		{"", 10 * time.Minute}, {"bogus", 10 * time.Minute}, {"0", time.Minute}, {"120", 2 * time.Minute}, {"999999", time.Hour},
		{now.Add(3 * time.Minute).Format(http.TimeFormat), 3 * time.Minute},
	} {
		if got := probeRetryAt(tc.header, now).Sub(now); got != tc.delay {
			t.Fatalf("%q: %v", tc.header, got)
		}
	}
}
