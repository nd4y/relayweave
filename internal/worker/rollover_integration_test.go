package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This exercises actual Xray -> Xray -> origin TCP/UDP, wall-clock canary,
// durable promotion and old associations. No Internet or provider is involved.
func TestRealSubscriptionRolloverPreservesTCPUDP(t *testing.T) {
	binary := os.Getenv("XRAY_EGRESS_TEST_BIN")
	if binary == "" {
		t.Skip("real core opt-in")
	}
	version := os.Getenv("XRAY_EGRESS_TEST_VERSION")
	ctx, cancel := context.WithTimeout(context.Background(), 160*time.Second)
	defer cancel()
	free := func() int {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return p
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	serverHost := ""
	for _, address := range addresses {
		ip, _, e := net.ParseCIDR(address.String())
		if e == nil && ip.To4() != nil && !ip.IsLoopback() {
			serverHost = ip.String()
			break
		}
	}
	if serverHost == "" {
		t.Fatal("isolated container private address required")
	}
	ports := []int{free(), free()}
	upstreams := []config.Route{}
	for i, port := range ports {
		upstreams = append(upstreams, config.Route{ID: strconv.Itoa(i), Endpoint: net.JoinHostPort("0.0.0.0", strconv.Itoa(port)), Outbound: json.RawMessage(`{"protocol":"freedom","settings":{}}`)})
	}
	rendered, err := core.Render(upstreams)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "upstream.json")
	if err = os.WriteFile(path, rendered, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "run", "-config", path)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); cmd.Wait() }()
	for _, port := range ports {
		deadline := time.Now().Add(5 * time.Second)
		for {
			conn, e := net.DialTimeout("tcp", net.JoinHostPort(serverHost, strconv.Itoa(port)), 100*time.Millisecond)
			if e == nil {
				conn.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("upstream unavailable")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer origin.Close()
	target := strings.Replace(origin.URL, "127.0.0.1", "origin.test", 1)
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, e := udp.ReadFromUDP(buf)
			if e != nil {
				return
			}
			if n >= 12 {
				buf[2] |= 0x80
			}
			udp.WriteToUDP(buf[:n], from)
		}
	}()
	file, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(file)
	r := NewRuntime(ctx, t.TempDir())
	r.RenewLease(time.Now().Add(time.Hour))
	g := New(ctx, config.Config{}, nil)
	g.Dynamic = r
	defer g.Close()
	socksListen := net.JoinHostPort("127.0.0.1", strconv.Itoa(free()))
	makeConfig := func(index int) config.Config {
		out, _ := json.Marshal(map[string]any{"protocol": "socks", "settings": map[string]any{"servers": []any{map[string]any{"address": serverHost, "port": ports[index]}}}})
		fp := sha256.Sum256(out)
		return config.Config{Managed: true, Generation: int64(index + 1), StateDir: t.TempDir(), MetricsListen: "127.0.0.1:0", ProbeURL: target, ConnectTimeoutMS: 2000, MaxConnections: 100, LeaseUntil: time.Now().Add(time.Hour), UDPProbeTargets: []string{udp.LocalAddr().String()}, Cores: []config.Core{{ID: "x", Binary: binary, Version: version, SHA256: hex.EncodeToString(digest[:])}}, Routes: []config.Route{{ID: "profile", Core: "x", Endpoint: "127.0.0.1:45000", ServerHost: serverHost, AllowPrivate: true, ServerAllowCIDRs: []string{serverHost + "/32"}, CheckServer: true, Outbound: out, Fingerprint: hex.EncodeToString(fp[:16]), ValidUntil: time.Now().Add(time.Hour), UDP: true}}, Policies: []config.Policy{{ID: "p", Mode: "auto", Routes: []string{"profile"}, ActivePaths: 1, ProbeURL: target, ProbeIntervalSeconds: 10, ProbeBudgetBytes: 1 << 20, ConnectTimeoutMS: 2000}}, Listeners: []config.Listener{{ID: "s", Protocol: "socks5", Listen: socksListen, Policy: "p", UDP: true}}, UDP: config.UDP{Bind: "127.0.0.1", Advertise: "127.0.0.1", PortMin: 36000, PortMax: 36031, IdleSeconds: 60}}
	}
	c := makeConfig(0)
	r.Restrict(c)
	if err = r.Apply(ctx, c, g); err != nil {
		t.Fatal(err)
	}
	a := &Agent{ctx: ctx, dir: t.TempDir(), runtime: r}
	if err = a.load(); err != nil {
		t.Fatal(err)
	}
	a.state.Config = &c
	a.state.Generation = c.Generation
	for range 3 {
		r.current["profile"].route.UDPAttempt.Store(0)
		a.probeUDPCapability(ctx)
	}
	if !udpProven(r.current["profile"].route) {
		t.Fatal("actual UDP health not proven")
	}
	echoTarget := "origin.test:" + strconv.Itoa(echo.Addr().(*net.TCPAddr).Port)
	oldTCP, old, err := g.dial(ctx, "policy:p", echoTarget)
	if err != nil {
		t.Fatal(err)
	}
	defer oldTCP.Close()
	assertEcho := func(conn net.Conn) {
		t.Helper()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		payload := []byte("persistent-sequence-payload")
		if _, e := conn.Write(payload); e != nil {
			t.Fatal(e)
		}
		got := make([]byte, len(payload))
		if _, e := io.ReadFull(conn, got); e != nil || !bytes.Equal(got, payload) {
			t.Fatal("persistent TCP interrupted", e)
		}
	}
	assertEcho(oldTCP)
	packets, err := (core.XrayAdapter{}).OpenUDP(ctx, socksListen, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer packets.Close()
	assertUDP := func() {
		t.Helper()
		packets.SetReadDeadline(time.Now().Add(3 * time.Second))
		if e := packets.WritePacket(core.Packet{Host: "origin.test", Port: udp.LocalAddr().(*net.UDPAddr).Port, Payload: []byte("echo")}); e != nil {
			t.Fatal(e)
		}
		packet, e := packets.ReadPacket(make([]byte, 2048))
		if e != nil || string(packet.Payload) != "echo" {
			t.Fatal("UDP association interrupted", e)
		}
	}
	assertUDP()
	nextConfig := makeConfig(1)
	nextConfig.Routes[0].Previous = []config.RevisionPermit{{Fingerprint: c.Routes[0].Fingerprint, ValidUntil: c.Routes[0].ValidUntil}}
	r.Restrict(nextConfig)
	if err = r.Apply(ctx, nextConfig, g); err != nil {
		t.Fatal(err)
	}
	a.state.Config = &nextConfig
	a.state.Generation = nextConfig.Generation
	admissions := 0
	started := time.Now()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("canary deadline", ctx.Err())
		default:
		}
		a.probeUDPCapability(ctx)
		r.mu.Lock()
		r.rebuildLocked()
		plan := r.rollovers["profile"]
		state := plan.state
		r.mu.Unlock()
		if r.rolloverDirty.Load() {
			a.mu.Lock()
			e := a.saveLocked()
			a.mu.Unlock()
			if e != nil {
				t.Fatal(e)
			}
		}
		assertEcho(oldTCP)
		assertUDP()
		if state == "active_draining" {
			break
		}
		conn, route, e := g.dial(ctx, "policy:p", echoTarget)
		if e != nil {
			t.Fatal(e)
		}
		assertEcho(conn)
		conn.Close()
		if route != old {
			admissions++
		}
		time.Sleep(20 * time.Millisecond)
	}
	if admissions < 60 || time.Since(started) < 90*time.Second {
		t.Fatal("unobserved/timer-only canary", admissions, time.Since(started))
	}
	for range 10 {
		conn, route, e := g.dial(ctx, "policy:p", echoTarget)
		if e != nil {
			t.Fatal(e)
		}
		if route == old {
			t.Fatal("old revision still admits after promotion")
		}
		assertEcho(conn)
		conn.Close()
	}
	assertEcho(oldTCP)
	assertUDP()
	fresh, err := (core.XrayAdapter{}).OpenUDP(ctx, socksListen, 2*time.Second)
	if err != nil {
		t.Fatal("new UDP association after promotion", err)
	}
	fresh.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err = fresh.WritePacket(core.Packet{Host: "origin.test", Port: udp.LocalAddr().(*net.UDPAddr).Port, Payload: []byte("new")}); err != nil {
		t.Fatal(err)
	}
	packet, err := fresh.ReadPacket(make([]byte, 2048))
	fresh.Close()
	if err != nil || string(packet.Payload) != "new" {
		t.Fatal("new UDP revision not working", err)
	}
	if old.Active.Load() < 2 {
		t.Fatal("old TCP/UDP not retained")
	}
	t.Logf("native wall-clock rollover: %d successful new admissions in %s; old TCP and UDP continuous", admissions, time.Since(started))
}
