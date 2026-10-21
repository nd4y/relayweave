package worker

import (
	"net"
	"net/http"
	"net/http/httptest"
	"proxyplatform/internal/config"
	"testing"
)

func TestUDPAutoBindsAndAnnouncesActualLANNetBirdOrIPv6Listener(t *testing.T) {
	for _, raw := range []string{"192.0.2.10", "100.64.0.10", "2001:db8::5", "127.0.0.1"} {
		bind, announce, e := udpRelayAddress(config.UDP{Bind: "0.0.0.0", Advertise: "auto"}, &net.TCPAddr{IP: net.ParseIP(raw), Port: 11080})
		if e != nil || bind != raw || announce != raw {
			t.Fatalf("%s: %s %s %v", raw, bind, announce, e)
		}
	}
	if _, _, e := udpRelayAddress(config.UDP{Advertise: "auto"}, &net.TCPAddr{IP: net.IPv4zero}); e == nil {
		t.Fatal("unspecified auto bind would expose relay")
	}
	bind, announce, e := udpRelayAddress(config.UDP{Bind: "0.0.0.0", Advertise: "192.0.2.4"}, &net.TCPAddr{IP: net.ParseIP("172.16.0.5")})
	if e != nil || bind != "0.0.0.0" || announce != "192.0.2.4" {
		t.Fatal("legacy bridge UDP changed")
	}
}
func TestHostMetricsAllowlistRejectsLANAndIgnoresForwardedHeader(t *testing.T) {
	t.Setenv("WORKER_METRICS_ALLOWED_CIDRS", "172.19.0.0/24")
	handler, e := metricsAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		remote string
		status int
	}{{"172.19.0.29:1234", 204}, {"172.19.0.81:1234", 204}, {"127.0.0.1:1234", 204}, {"192.168.0.5:1234", 403}, {"[2001:db8::1]:1234", 403}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/status", nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Forwarded-For", "172.19.0.29")
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code)
		}
	}
	t.Setenv("WORKER_METRICS_ALLOWED_CIDRS", "any")
	if _, e := metricsAccess(handler); e == nil {
		t.Fatal("invalid ACL accepted")
	}
}
