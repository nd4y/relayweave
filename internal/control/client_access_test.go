package control

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http/httptest"
	"net/url"
	"proxyplatform/internal/catalog"
	"strconv"
	"strings"
	"testing"
)

func TestAccessURIsPreserveIPv6UnicodeCredentialsAndOnlyOwnEndpoint(t *testing.T) {
	for _, proto := range []string{"http", "socks5"} {
		l := catalog.Listener{Enabled: true, Protocol: proto, Listen: "0.0.0.0:18080", Username: "u:@ /я", Password: "unused"}
		password := "p:@/% ?&=я"
		doc := accessDocument("Дом", "2001:db8::1", l, password)
		parsed, e := url.Parse(doc["uri"].(string))
		if e != nil || parsed.Hostname() != "2001:db8::1" || parsed.Port() != "18080" || parsed.User.Username() != l.Username {
			t.Fatal("URI identity lost")
		}
		got, _ := parsed.User.Password()
		if got != password {
			t.Fatal("URI password encoding")
		}
		raw, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(doc["throne_uri"].(string), "throne://add/"))
		if e != nil {
			t.Fatal(e)
		}
		var out map[string]any
		if json.Unmarshal(raw, &out) != nil || out["server"] != "2001:db8::1" || out["password"] != password {
			t.Fatal("Throne native payload")
		}
		allowed := map[string]bool{"type": true, "tag": true, "server": true, "server_port": true, "username": true, "password": true, "version": true}
		for k := range out {
			if !allowed[k] {
				t.Fatal("unexpected export field", k)
			}
		}
		var config struct {
			Outbounds []map[string]any `json:"outbounds"`
		}
		if json.Unmarshal([]byte(doc["config"].(string)), &config) != nil || len(config.Outbounds) != 1 {
			t.Fatal("sing-box subscription schema")
		}
	}
}
func TestClientAccessRejectsPrivateUnpublishedListenerAndInvalidHost(t *testing.T) {
	t.Setenv("CLIENT_PUBLIC_PORTS", "18080,11080")
	for _, port := range []string{"8082", "8083", "8084", "9090", "18081"} {
		if _, ok := accessListener(catalog.Listener{Enabled: true, Protocol: "http", Listen: "0.0.0.0:" + port}); ok {
			t.Fatal("private listener export", port)
		}
	}
	for _, host := range []string{"a/path", "user@host", "x?token=secret", "[::1]/x", "host\r\nInjected:"} {
		if cleanAccessHost(host) != "" {
			t.Fatal("unsafe host", host)
		}
	}
}
func TestDefaultListenerPortContract(t *testing.T) {
	t.Setenv("CLIENT_DEFAULT_BIND_IP", "")
	for _, custom := range []bool{false, true} {
		hp, sp := 18080, 11080
		t.Setenv("CLIENT_DEFAULT_HTTP_PORT", "")
		t.Setenv("CLIENT_DEFAULT_SOCKS_PORT", "")
		if custom {
			hp, sp = 23880, 23881
			t.Setenv("CLIENT_DEFAULT_HTTP_PORT", "23880")
			t.Setenv("CLIENT_DEFAULT_SOCKS_PORT", "23881")
		}
		for _, tc := range []struct {
			protocol string
			port     int
			udp      bool
		}{{"http", hp, false}, {"socks5", sp, true}} {
			got := defaultAccessListener(tc.protocol)
			if !got.Enabled || got.Protocol != tc.protocol || got.UDP != tc.udp || got.Listen != net.JoinHostPort("0.0.0.0", strconv.Itoa(tc.port)) {
				t.Fatalf("fresh deployment listener contract %+v", got)
			}
		}
	}
	t.Setenv("CLIENT_DEFAULT_BIND_IP", "192.0.2.10")
	if got := defaultAccessListener("http"); !strings.HasPrefix(got.Listen, "192.0.2.10:") {
		t.Fatal("host mode default widened bind")
	}
}

func TestClientExportRequiresAdminSessionAndInvalidTokenIsNotAnAPIBypass(t *testing.T) {
	s := &Server{}
	for _, path := range []string{"/api/v1/client-access", "/api/v1/client-access/render", "/api/v1/client-access/subscriptions"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatal(path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/client-sub/invalid", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestAccessHostInterfaceAndTLSMappingAreNotInterchangeable(t *testing.T) {
	t.Setenv("CLIENT_ACCESS_HOSTS", "LAN=192.0.2.10,VPN=100.106.1.2")
	t.Setenv("CLIENT_HTTPS_PROXY_HOST", "proxy.example.test")
	t.Setenv("CLIENT_HTTPS_LISTENER_ID", "tls-private")
	s := &Server{}
	l := catalog.Listener{Enabled: true, Protocol: "http", Listen: "192.0.2.10:18080"}
	if !s.accessCompatible(t.Context(), "lan", l, "192.0.2.10") || s.accessCompatible(t.Context(), "lan", l, "100.106.1.2") {
		t.Fatal("wrong interface exported")
	}
	if s.accessCompatible(t.Context(), "lan", l, "proxy.example.test") {
		t.Fatal("TLS mapped to wrong listener")
	}
	l.Listen = "172.19.0.1:18080"
	if !s.accessCompatible(t.Context(), "tls-private", l, "proxy.example.test") {
		t.Fatal("configured TLS backend rejected")
	}
	doc := accessDocument("TLS", "proxy.example.test", l, "")
	u, e := url.Parse(doc["uri"].(string))
	if e != nil || u.Scheme != "https" || u.Port() != "443" {
		t.Fatal("TLS external port is not Docker mapping")
	}
}
