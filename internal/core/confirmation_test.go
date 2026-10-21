package core

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"proxyplatform/internal/config"
)

type directHealthFixture struct{ XrayAdapter }

func (directHealthFixture) OpenTCP(ctx context.Context, _, target string, timeout time.Duration) (net.Conn, error) {
	return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", target)
}

func TestConfirmationTimeoutRetainsSlowAndUnknownPathBudgets(t *testing.T) {
	for _, test := range []struct {
		name         string
		latency      time.Duration
		configuredMS int
		confirmation bool
		want         time.Duration
	}{
		{"regular check", 10 * time.Millisecond, 3000, false, 3 * time.Second},
		{"unknown latency", 0, 3000, true, 3 * time.Second},
		{"known fast path", 10 * time.Millisecond, 3000, true, time.Second},
		{"known slow path", 400 * time.Millisecond, 3000, true, 1850 * time.Millisecond},
		{"configured ceiling", time.Second, 3000, true, 3 * time.Second},
		{"short explicit deadline", 10 * time.Millisecond, 500, true, 500 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			i := &Instance{}
			i.Ready.Store(true)
			i.LatencyUS.Store(test.latency.Microseconds())
			got := i.healthTimeout(config.Config{ConnectTimeoutMS: test.configuredMS}, test.confirmation)
			if got != test.want {
				t.Fatalf("health budget=%s want=%s", got, test.want)
			}
		})
	}
	i := &Instance{}
	i.LatencyUS.Store((10 * time.Millisecond).Microseconds())
	if got := i.healthTimeout(config.Config{ConnectTimeoutMS: 3000}, true); got != 3*time.Second {
		t.Fatalf("unhealthy path lost its full recovery budget: %s", got)
	}
}

func TestIndependentConfirmationBoundsBlackholeAndAcceptsSlowHealthyTarget(t *testing.T) {
	blackhole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer blackhole.Close()
	fast := &Instance{}
	fast.Ready.Store(true)
	fast.LatencyUS.Store((10 * time.Millisecond).Microseconds())
	started := time.Now()
	if probe(context.Background(), directHealthFixture{}, "", blackhole.URL, fast.healthTimeout(config.Config{ConnectTimeoutMS: 3000}, true)) {
		t.Fatal("blackhole was healthy")
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("known fast path confirmation retained a slow timeout: %s", elapsed)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		w.Write([]byte("healthy"))
	}))
	defer slow.Close()
	i := &Instance{}
	i.Ready.Store(true)
	i.LatencyUS.Store((400 * time.Millisecond).Microseconds())
	if !probe(context.Background(), directHealthFixture{}, "", slow.URL, i.healthTimeout(config.Config{ConnectTimeoutMS: 3000}, true)) {
		t.Fatal("confirmation falsely rejected a known slow healthy target")
	}
}
