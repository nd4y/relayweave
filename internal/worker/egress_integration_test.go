package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/socks"
)

// Run in an isolated Linux container with --add-host=origin.test:127.0.0.1.
// Addresses below are synthetic egress lanes, not real ISP throughput tests.
func TestRealEgressHTTPAndSOCKSAndDisable(t *testing.T) {
	binary := os.Getenv("XRAY_EGRESS_TEST_BIN")
	if binary == "" {
		t.Skip("real core opt-in")
	}
	version := os.Getenv("XRAY_EGRESS_TEST_VERSION")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		if r.URL.Path == "/health" {
			w.WriteHeader(204)
			return
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		fmt.Fprintln(w, ip)
	}))
	defer origin.Close()
	target := strings.Replace(origin.URL, "127.0.0.1", "origin.test", 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := NewRuntime(ctx, t.TempDir())
	runtime.RenewLease(time.Now().Add(time.Hour))
	g := New(ctx, config.Config{}, nil)
	g.Dynamic = runtime
	defer g.Close()
	free := func() string {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		a := l.Addr().String()
		l.Close()
		return a
	}
	httpListen, socksListen := free(), free()
	makeConfig := func(disable bool) config.Config {
		out := json.RawMessage(`{"protocol":"freedom","settings":{}}`)
		c := config.Config{Generation: 1, StateDir: t.TempDir(), MetricsListen: "127.0.0.1:0", ProbeURL: target + "/health", LeaseUntil: time.Now().Add(time.Hour), DownloadMbps: 100, UploadMbps: 100, MaxConnections: 100, ConnectTimeoutMS: 2000, Cores: []config.Core{{ID: "x", Version: version, Binary: binary}}, Routes: []config.Route{{ID: "profile", Core: "x", Endpoint: "127.0.0.1:45000", Outbound: out, UDP: true}}, Policies: []config.Policy{{ID: "p", Mode: "auto", Routes: []string{"profile"}, ActivePaths: 2, ProbeURL: target + "/health", ConnectTimeoutMS: 2000}}, Listeners: []config.Listener{{ID: "h", Protocol: "http", Listen: httpListen, Policy: "p"}, {ID: "s", Protocol: "socks5", Listen: socksListen, Policy: "p", UDP: true}}, UDP: config.UDP{Bind: "127.0.0.1", Advertise: "127.0.0.1", PortMin: 35000, PortMax: 35015}, EgressPaths: []config.EgressPath{{ID: "isp1", SourceIP: "127.0.0.2", Enabled: !disable, Weight: 8, DownloadMbps: 80, UploadMbps: 80}, {ID: "isp2", SourceIP: "127.0.0.3", Enabled: true, Weight: 5, DownloadMbps: 50, UploadMbps: 50}}}
		if disable {
			c.Generation = 2
		}
		if e := c.ExpandEgress(); e != nil {
			t.Fatal(e)
		}
		return c
	}
	c := makeConfig(false)
	runtime.Restrict(c)
	if e := runtime.Apply(ctx, c, g); e != nil {
		t.Fatal(e)
	}
	runtime.applyMu.Lock()
	for _, route := range c.Routes {
		entry, e := runtime.ensure(ctx, c, route)
		if e != nil {
			runtime.applyMu.Unlock()
			t.Fatal(e)
		}
		runtime.mu.Lock()
		runtime.current[route.ID] = entry
		runtime.mu.Unlock()
		if !entry.route.Instance.CheckRoute(ctx, entry.route.Config.Endpoint, c) {
			runtime.applyMu.Unlock()
			t.Fatal("second ISP probe failed")
		}
		entry.route.Instance.Ready.Store(true)
	}
	runtime.applyMu.Unlock()
	runtime.mu.Lock()
	runtime.rebuildLocked()
	runtime.mu.Unlock()
	counts := map[string]int{}
	proxy, _ := url.Parse("http://" + httpListen)
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for i := 0; i < 26; i++ {
		response, e := client.Get(target)
		if e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(response.Body)
		response.Body.Close()
		if e != nil || response.StatusCode != 200 {
			t.Fatal("HTTP failed", e)
		}
		counts[strings.TrimSpace(string(body))]++
	}
	if counts["127.0.0.2"] < 10 || counts["127.0.0.3"] < 7 {
		t.Fatal("both ISP lanes not used by HTTP", counts)
	}
	u, _ := url.Parse(target)
	conn, e := socks.Dial(ctx, socksListen, u.Host, 2*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: origin.test\r\nConnection: close\r\n\r\n")
	reply, e := io.ReadAll(conn)
	conn.Close()
	if e != nil || !strings.Contains(string(reply), "200 OK") {
		t.Fatal("SOCKS TCP failed", e)
	}
	udp, e := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if e != nil {
		t.Fatal(e)
	}
	defer udp.Close()
	go func() {
		b := make([]byte, 4096)
		for {
			_, from, e := udp.ReadFromUDP(b)
			if e != nil {
				return
			}
			udp.WriteToUDP([]byte(from.IP.String()), from)
		}
	}()
	udpCounts := map[string]int{}
	for i := 0; i < 13; i++ {
		control, host, port, e := socks.Request(ctx, socksListen, 3, "0.0.0.0", 0, 2*time.Second)
		if e != nil {
			t.Fatal(e)
		}
		down, e := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP(host), Port: port})
		if e != nil {
			control.Close()
			t.Fatal(e)
		}
		down.SetDeadline(time.Now().Add(3 * time.Second))
		address, _ := socks.Address("origin.test", udp.LocalAddr().(*net.UDPAddr).Port)
		_, e = down.Write(append(append([]byte{0, 0, 0}, address...), []byte("payload")...))
		if e != nil {
			t.Fatal(e)
		}
		buffer := make([]byte, 4096)
		n, e := down.Read(buffer)
		down.Close()
		control.Close()
		if e != nil {
			t.Fatal("SOCKS UDP failed", e)
		}
		reader := bytes.NewReader(buffer[3:n])
		if _, _, _, e = socks.ReadAddress(reader); e != nil {
			t.Fatal(e)
		}
		body, _ := io.ReadAll(reader)
		udpCounts[string(body)]++
	}
	if udpCounts["127.0.0.2"] == 0 || udpCounts["127.0.0.3"] == 0 {
		t.Fatal("SOCKS UDP did not use both paths", udpCounts)
	}
	// Disable one lane in the desired revision. Its old processes may drain but
	// no candidate can use them after the admission restriction is installed.
	next := makeConfig(true)
	runtime.Restrict(next)
	if e = runtime.Apply(ctx, next, g); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 10; i++ {
		response, e := client.Get(target)
		if e != nil {
			t.Fatal(e)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if strings.TrimSpace(string(body)) != "127.0.0.3" {
			t.Fatal("disabled ISP still allocated")
		}
	}
	if len(runtime.Configuration().Routes) != 1 {
		t.Fatal("disabled ISP still in catalogue")
	}
	t.Logf("HTTP peers: %v; SOCKS UDP peers: %v; SOCKS TCP passed; disabled ISP received no new requests", counts, udpCounts)
}
