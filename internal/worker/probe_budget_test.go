package worker

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
)

type probeBudgetFixture struct{ core.XrayAdapter }

func (probeBudgetFixture) Family() string { return "worker-probe-budget-test" }
func (probeBudgetFixture) OpenTCP(ctx context.Context, _, target string, timeout time.Duration) (net.Conn, error) {
	return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", target)
}

func TestAcceptedSlowPathRetainsItsConfiguredHealthBudget(t *testing.T) {
	if _, err := core.AdapterFor("worker-probe-budget-test"); err != nil {
		if err := core.RegisterAdapter(probeBudgetFixture{}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3500 * time.Millisecond):
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	i := &core.Instance{Config: config.Core{Family: "worker-probe-budget-test"}}
	candidate := config.Config{ProbeURL: server.URL, ConnectTimeoutMS: 6000}
	if !i.CheckRoute(context.Background(), server.Listener.Addr().String(), candidate) {
		t.Fatal("healthy slow candidate rejected")
	}
	c := config.Config{ProbeURL: server.URL, ConnectTimeoutMS: 3000, Policies: []config.Policy{{ID: "slow", Routes: []string{"route"}, ProbeURL: server.URL, ConnectTimeoutMS: 6000}}}
	committed := routeProbeConfig(c, "route")
	if !i.CheckRoute(context.Background(), server.Listener.Addr().String(), committed) {
		t.Fatal("accepted slow path loses its configured probe timeout after commit")
	}
}

func TestProbeBudgetUsesOnlyRelevantPoliciesAndKeepsExplicitShortTimeout(t *testing.T) {
	c := config.Config{ProbeURL: "https://health.invalid/", ConnectTimeoutMS: 3000, Policies: []config.Policy{
		{ID: "short", Routes: []string{"route"}, ConnectTimeoutMS: 1000},
		{ID: "unrelated", Routes: []string{"other"}, ConnectTimeoutMS: 30000},
	}}
	if got := routeProbeConfig(c, "route").ConnectTimeoutMS; got != 1000 {
		t.Fatalf("explicit short budget=%d", got)
	}
	c.Policies = append(c.Policies, config.Policy{ID: "default", Routes: []string{"route"}})
	if got := routeProbeConfig(c, "route").ConnectTimeoutMS; got != 3000 {
		t.Fatalf("referencing policy lost its global fallback budget=%d", got)
	}
	c.Policies = append(c.Policies, config.Policy{ID: "slow", Routes: []string{"route"}, ConnectTimeoutMS: 6000})
	if got := routeProbeConfig(c, "route").ConnectTimeoutMS; got != 6000 {
		t.Fatalf("shared relevant budget=%d", got)
	}
	if got := routeProbeConfig(c, "absent").ConnectTimeoutMS; got != 3000 {
		t.Fatalf("global fallback budget=%d", got)
	}
}
