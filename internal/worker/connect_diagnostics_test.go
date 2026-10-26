package worker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/socks"
)

const defaultConnectResponse = "HTTP/1.1 200 Connection Established\r\n\r\n"

func diagnosticTestRoute() *Route {
	return &Route{Config: config.Route{ID: "route_a-1", ProfileID: "profile.b-2", EgressID: "isp_3"}}
}

func TestConnectDiagnosticsDefaultOff(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		policy := loadConnectDiagnosticPolicy(raw)
		if policy.enabled {
			t.Fatalf("%q enabled diagnostics", raw)
		}
		if got := policy.response("192.0.2.10:443", diagnosticTestRoute()); got != defaultConnectResponse {
			t.Fatalf("%q changed default response: %q", raw, got)
		}
	}
}

func TestConnectDiagnosticsInitializedFromEnvironment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Setenv(connectDiagnosticsEnv, "192.0.2.0/24")
	g := New(ctx, config.Config{}, nil)
	if !g.diagnostics.enabled || !g.diagnostics.allows("192.0.2.10:443") {
		t.Fatal("configured diagnostics not loaded by New")
	}
}

func TestConnectDiagnosticsPeerAllowlist(t *testing.T) {
	policy := loadConnectDiagnosticPolicy("192.0.2.0/24,2001:db8::/32")
	if !policy.enabled {
		t.Fatal("valid allowlist rejected")
	}
	for _, tc := range []struct {
		remote  string
		allowed bool
	}{
		{"192.0.2.10:443", true},
		{"[::ffff:192.0.2.10]:443", true},
		{"[2001:db8::1]:443", true},
		{"198.51.100.10:443", false},
		{"[2001:db9::1]:443", false},
		{"[::ffff:198.51.100.10]:443", false},
		{"192.0.2.10", false},
		{"192.0.2.10:0", false},
		{"192.0.2.10:65536", false},
		{"192.0.2.10:http", false},
		{"[192.0.2.10]:443", true},
		{"192.0.2.10:443\r\nX-Forwarded-For: 192.0.2.10", false},
		{"", false},
		{"unknown", false},
	} {
		if got := policy.allows(tc.remote); got != tc.allowed {
			t.Fatalf("%q allowed=%v want=%v", tc.remote, got, tc.allowed)
		}
	}
}

// Exercise actual CONNECT serialization and transparent tunnel bytes. Request
// headers cannot opt a client in; listener authentication still runs first.
func TestConnectDiagnosticsWireAndAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, allow string
		auth        bool
		status      int
		diagnostics bool
	}{
		{"default", "", false, 200, false},
		{"allowed", "127.0.0.1/32", false, 200, true},
		{"spoofed", "192.0.2.10/32", false, 200, false},
		{"unauthorized", "127.0.0.1/32", true, 407, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			adapter, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				peer, e := adapter.Accept()
				if e != nil {
					return
				}
				defer peer.Close()
				peer.SetDeadline(time.Now().Add(3 * time.Second))
				if _, e = io.ReadFull(peer, make([]byte, 3)); e != nil {
					return
				}
				if e = socks.Write(peer, []byte{5, 0}); e != nil {
					return
				}
				if _, e = io.ReadFull(peer, make([]byte, 3)); e != nil {
					return
				}
				if _, _, _, e = socks.ReadAddress(peer); e != nil {
					return
				}
				if e = socks.Reply(peer, 0, "127.0.0.1", 1); e != nil {
					return
				}
				io.Copy(peer, peer)
			}()
			t.Cleanup(func() { adapter.Close(); <-done })
			g := New(ctx, config.Config{ConnectTimeoutMS: 1000}, nil)
			g.diagnostics = loadConnectDiagnosticPolicy(tc.allow)
			instance := &core.Instance{}
			instance.Ready.Store(true)
			route := diagnosticTestRoute()
			route.Config.Endpoint = adapter.Addr().String()
			route.Instance = instance
			g.Routes = []*Route{route}
			listener := config.Listener{ID: "test"}
			if tc.auth {
				listener.Username = "fixture"
				listener.Password = "fixture"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { g.httpProxy(listener, nil, w, r) }))
			defer server.Close()
			conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			fmt.Fprint(conn, "CONNECT origin.test:443 HTTP/1.1\r\nHost: origin.test:443\r\nX-Forwarded-For: 192.0.2.10\r\nForwarded: for=192.0.2.10\r\nX-RelayWeave-Route-ID: injected\r\n\r\n")
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != tc.status {
				t.Fatalf("status %d", response.StatusCode)
			}
			if tc.diagnostics {
				if response.Header.Get(connectRouteHeader) != route.Config.ID || response.Header.Get(connectProfileHeader) != route.Config.Profile() || response.Header.Get(connectISPHeader) != route.Config.EgressID {
					t.Fatalf("wrong actual route: %v", response.Header)
				}
			} else {
				for _, name := range []string{connectRouteHeader, connectProfileHeader, connectISPHeader} {
					if response.Header.Get(name) != "" {
						t.Fatalf("diagnostics leaked: %s", name)
					}
				}
			}
			if tc.status == 200 {
				payload := "opaque-tunnel-payload"
				fmt.Fprint(conn, payload)
				got := make([]byte, len(payload))
				if _, err = io.ReadFull(reader, got); err != nil || string(got) != payload {
					t.Fatalf("tunnel changed: %q %v", got, err)
				}
			}
		})
	}
}

func TestConnectDiagnosticsMappedIPv4Prefix(t *testing.T) {
	policy := loadConnectDiagnosticPolicy("::ffff:192.0.2.0/120")
	if !policy.enabled || !policy.allows("192.0.2.10:443") || !policy.allows("[::ffff:192.0.2.10]:443") {
		t.Fatal("mapped IPv4 prefix not normalized")
	}
	if policy.allows("198.51.100.10:443") {
		t.Fatal("mapped IPv4 prefix widened")
	}
}

func TestConnectDiagnosticsInvalidCIDRFailsClosed(t *testing.T) {
	for _, raw := range []string{
		"192.0.2.0/24,not-a-cidr",
		"192.0.2.0/24,",
		"any",
		"192.0.2.0/33",
		"192.0.2.0/24,::ffff:0:0/80",
		"192.0.2.0/24,2001:db8::/129",
	} {
		policy := loadConnectDiagnosticPolicy(raw)
		if policy.enabled {
			t.Fatalf("%q accepted", raw)
		}
		if got := policy.response("192.0.2.10:443", diagnosticTestRoute()); got != defaultConnectResponse {
			t.Fatalf("%q exposed diagnostics", raw)
		}
	}
}

func TestConnectDiagnosticsRejectsInvalidIdentifiers(t *testing.T) {
	policy := loadConnectDiagnosticPolicy("192.0.2.0/24")
	valid := diagnosticTestRoute()
	for name, route := range map[string]*Route{
		"nil":             nil,
		"route injection": {Config: config.Route{ID: "route\r\nX-Injected: 1", ProfileID: "p", EgressID: "i"}},
		"profile space":   {Config: config.Route{ID: "r", ProfileID: "profile with space", EgressID: "i"}},
		"isp slash":       {Config: config.Route{ID: "r", ProfileID: "p", EgressID: "isp/1"}},
		"empty isp":       {Config: config.Route{ID: "r", ProfileID: "p"}},
		"long route":      {Config: config.Route{ID: strings.Repeat("r", connectIdentifierMax+1), ProfileID: "p", EgressID: "i"}},
		"non ascii":       {Config: config.Route{ID: "routé", ProfileID: "p", EgressID: "i"}},
	} {
		got := policy.response("192.0.2.10:443", route)
		if got != defaultConnectResponse {
			t.Fatalf("%s exposed invalid route diagnostics: %q", name, got)
		}
		if strings.Contains(got, "X-RelayWeave") {
			t.Fatalf("%s leaked diagnostic header", name)
		}
	}
	if got := policy.response("192.0.2.10:443", valid); got == defaultConnectResponse {
		t.Fatal("valid route identifiers omitted")
	}
}

func TestConnectDiagnosticsResponseUsesActualRouteProfileAndISP(t *testing.T) {
	policy := loadConnectDiagnosticPolicy("192.0.2.0/24")
	got := policy.response("192.0.2.10:443", diagnosticTestRoute())
	want := "HTTP/1.1 200 Connection Established\r\n" +
		"X-RelayWeave-Route-ID: route_a-1\r\n" +
		"X-RelayWeave-Profile-ID: profile.b-2\r\n" +
		"X-RelayWeave-ISP: isp_3\r\n\r\n"
	if got != want {
		t.Fatalf("response = %q want %q", got, want)
	}
	if strings.Contains(got, "192.0.2.10") || strings.Contains(got, "443") {
		t.Fatal("peer or target exposed in response")
	}
}

func TestConnectDiagnosticsProfileFallsBackToRouteID(t *testing.T) {
	policy := loadConnectDiagnosticPolicy("192.0.2.0/24")
	route := &Route{Config: config.Route{ID: "route_only", EgressID: "isp"}}
	got := policy.response("192.0.2.10:443", route)
	want := "HTTP/1.1 200 Connection Established\r\n" +
		"X-RelayWeave-Route-ID: route_only\r\n" +
		"X-RelayWeave-Profile-ID: route_only\r\n" +
		"X-RelayWeave-ISP: isp\r\n\r\n"
	if got != want {
		t.Fatalf("response = %q want %q", got, want)
	}
}

func TestConnectDiagnosticsExcludedPeerKeepsDefaultResponse(t *testing.T) {
	policy := loadConnectDiagnosticPolicy("192.0.2.0/24")
	spoofed := "198.51.100.10:443\r\nX-Forwarded-For: 192.0.2.10"
	if got := policy.response(spoofed, diagnosticTestRoute()); got != defaultConnectResponse {
		t.Fatalf("excluded peer got diagnostics: %q", got)
	}
	if got := policy.response("198.51.100.10:443", diagnosticTestRoute()); got != defaultConnectResponse {
		t.Fatalf("excluded peer got diagnostics: %q", got)
	}
}
