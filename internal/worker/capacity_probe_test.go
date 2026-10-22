package worker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/wire"
)

func TestAutomaticProbeReservesAndReadsOnlyItsBoundBeforePersistingEstimate(t *testing.T) {
	if _, err := core.AdapterFor("worker-probe-budget-test"); err != nil {
		if err := core.RegisterAdapter(probeBudgetFixture{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, automatic := range []bool{true, false} {
		t.Run(strconv.FormatBool(automatic), func(t *testing.T) {
			a := &Agent{dir: t.TempDir(), ctx: context.Background()}
			if err := a.load(); err != nil {
				t.Fatal(err)
			}
			r := classifiedBudgetFixture()
			a.runtime = r
			r.lease.Store(time.Now().Add(time.Hour).Unix())
			limit := int64(8 << 20)
			if automatic {
				limit = automaticCapacityProbeBytes
			}
			requests := make(chan string, 1)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests <- req.Header.Get("Range")
				// A distant origin must affect RTT, not the payload capacity.
				time.Sleep(150 * time.Millisecond)
				// Deliberately ignore Range. The client must bound actual reads.
				w.Header().Set("Content-Length", strconv.Itoa(8<<20))
				_, _ = w.Write([]byte(strings.Repeat("x", 8<<20)))
			}))
			defer origin.Close()
			private, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer private.Close()
			c := config.Config{Cores: []config.Core{{ID: "x", Version: "test"}},
				Routes:   []config.Route{{ID: "route", Core: "x", Endpoint: private.Addr().String(), Outbound: json.RawMessage(`{"protocol":"freedom"}`)}},
				Policies: []config.Policy{{ID: "p", Routes: []string{"route"}, SpeedURL: origin.URL, ProbeBudgetBytes: 128 << 20}}}
			r.config = c
			key := routeIdentity(c.Routes[0], "test/")
			e := &engineEntry{key: key, route: &Route{Config: c.Routes[0], Instance: &core.Instance{Config: config.Core{Family: "worker-probe-budget-test"}}}}
			r.entries = map[string]*engineEntry{key: e}
			payload, _ := json.Marshal(profileProbe{ProbeURL: origin.URL, SpeedURL: origin.URL, PolicyID: "p", Budget: 128 << 20, Automatic: automatic})
			result, err := a.test(context.Background(), wire.Command{Action: "speed-test", Target: "route", Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			if got := <-requests; got != "bytes=0-"+strconv.FormatInt(limit-1, 10) {
				t.Fatalf("range=%s", got)
			}
			if result.(map[string]any)["bytes"] != limit || r.budget["p"] != limit || r.capacityBudget["p"] != limit || r.probeBytes.Load() != limit {
				t.Fatal("I/O exceeded its durable reservation or manual bound changed")
			}
			measurement := result.(map[string]any)
			wholeRequestBPS := float64(limit) / (measurement["elapsed_ms"].(float64) / 1000)
			if float64(e.route.SpeedBPS.Load()) <= wholeRequestBPS*2 || measurement["ttfb_ms"].(float64) < 140 {
				t.Fatal("payload capacity still includes setup latency")
			}
			b := &Agent{dir: a.dir}
			if err := b.load(); err != nil {
				t.Fatal(err)
			}
			if b.state.ProbeBudget["p"] != limit || b.state.ProbeCapacityBudget["p"] != limit || len(b.state.CapacityHints) != 1 {
				t.Fatal("successful estimate/budget did not survive encrypted reload")
			}
		})
	}
}
