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
	"sync/atomic"
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
	soak := rolloverSoakDuration(t)
	ctx, cancel := context.WithTimeout(context.Background(), soak+240*time.Second)
	defer cancel()
	diagnostic := newRolloverDiagnostics(os.Getenv("ROLLOVER_DIAGNOSTICS_PATH"))
	var diagnosticPID atomic.Pointer[atomic.Int64]
	originPID := func() int64 {
		if pid := diagnosticPID.Load(); pid != nil {
			return pid.Load()
		}
		return 0 // No original engine has been selected yet.
	}
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
	ports := []int{free(), free(), free(), free(), free()}
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
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bulk" {
			w.Header().Set("Content-Length", "1048576")
			io.CopyN(w, strings.NewReader(strings.Repeat("b", int(qualityBulkPayload))), qualityBulkPayload)
			return
		}
		w.WriteHeader(204)
	}))
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
	var persistentUDPPeer atomic.Value
	var changedUDPPeer atomic.Bool
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, e := udp.ReadFromUDP(buf)
			if e != nil {
				if diagnostic != nil {
					diagnostic.record("origin_recv_error", 0, originPID(), "", false, 0, e)
				}
				return
			}
			if n == 29 && buf[2] == 1 && buf[5] == 1 {
				buf[2] |= 0x80
			}
			heldPacket := bytes.HasPrefix(buf[:n], []byte("persistent-udp-sequence-"))
			var sequence uint64
			if heldPacket {
				if diagnostic != nil {
					sequence, _ = strconv.ParseUint(string(buf[len("persistent-udp-sequence-"):n]), 10, 64)
				}
				if peer := persistentUDPPeer.Load(); peer == nil {
					persistentUDPPeer.Store(from.String())
				} else if peer.(string) != from.String() {
					changedUDPPeer.Store(true)
				}
				if diagnostic != nil {
					diagnostic.record("origin_recv", sequence, originPID(), from.String(), changedUDPPeer.Load(), n, nil)
				}
			}
			written, writeErr := udp.WriteToUDP(buf[:n], from)
			if diagnostic != nil && heldPacket {
				if writeErr == nil && written != n {
					writeErr = io.ErrShortWrite
				}
				diagnostic.record("origin_send", sequence, originPID(), from.String(), changedUDPPeer.Load(), written, writeErr)
			}
		}
	}()
	file, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(file)
	r := NewRuntime(ctx, t.TempDir())
	horizon := time.Now().Add(soak + time.Hour)
	r.RenewLease(horizon)
	g := New(ctx, config.Config{}, nil)
	g.Dynamic = r
	defer g.Close()
	socksListen := net.JoinHostPort("127.0.0.1", strconv.Itoa(free()))
	makeConfig := func(index int) config.Config {
		out, _ := json.Marshal(map[string]any{"protocol": "socks", "settings": map[string]any{"servers": []any{map[string]any{"address": serverHost, "port": ports[index]}}}})
		fp := sha256.Sum256(out)
		return config.Config{Managed: true, Generation: int64(index + 1), StateDir: t.TempDir(), MetricsListen: "127.0.0.1:0", ProbeURL: target, ConnectTimeoutMS: 2000, MaxConnections: 100, LeaseUntil: horizon, UDPProbeTargets: []string{udp.LocalAddr().String()}, Cores: []config.Core{{ID: "x", Binary: binary, Version: version, SHA256: hex.EncodeToString(digest[:])}}, Routes: []config.Route{{ID: "profile", Core: "x", Endpoint: "127.0.0.1:45000", ServerHost: serverHost, AllowPrivate: true, ServerAllowCIDRs: []string{serverHost + "/32"}, CheckServer: true, Outbound: out, Fingerprint: hex.EncodeToString(fp[:16]), ValidUntil: horizon, UDP: true}}, Policies: []config.Policy{{ID: "p", Mode: "auto", Routes: []string{"profile"}, ActivePaths: 1, ProbeURL: target, ProbeIntervalSeconds: 10, DisableSpeedProbes: true, ProbeBudgetBytes: 16 << 20, ConnectTimeoutMS: 2000}}, Listeners: []config.Listener{{ID: "s", Protocol: "socks5", Listen: socksListen, Policy: "p", UDP: true}}, UDP: config.UDP{Bind: "127.0.0.1", Advertise: "127.0.0.1", PortMin: 36000, PortMax: 36031, IdleSeconds: 60}}
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
	a.mu.Lock()
	a.state.Config = &c
	a.state.Generation = c.Generation
	a.mu.Unlock()
	loopsDone := make(chan struct{}, 2)
	go func() { a.udpProbes(); loopsDone <- struct{}{} }()
	go func() { a.rolloverQualityLoop(); loopsDone <- struct{}{} }()
	defer func() { cancel(); <-loopsDone; <-loopsDone }()
	deadline := time.Now().Add(30 * time.Second)
	for !udpProven(r.current["profile"].route) {
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatal("production UDP scheduler did not establish capability")
		}
		time.Sleep(50 * time.Millisecond)
	}
	echoTarget := "origin.test:" + strconv.Itoa(echo.Addr().(*net.TCPAddr).Port)
	oldTCP, old, err := g.dial(ctx, "policy:p", echoTarget)
	if err != nil {
		t.Fatal(err)
	}
	defer oldTCP.Close()
	var adhocSequence uint64
	assertEcho := func(conn net.Conn) {
		t.Helper()
		adhocSequence++
		if e := rolloverEchoSequence(conn, adhocSequence); e != nil {
			t.Fatal(e)
		}
	}
	packets, err := (core.XrayAdapter{}).OpenUDP(ctx, socksListen, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer packets.Close()
	oldPID := old.Instance.PID.Load()
	if oldPID == 0 {
		t.Fatal("old engine process missing")
	}
	if diagnostic != nil {
		diagnosticPID.Store(&old.Instance.PID)
	}
	heldCtx, stopHeld := context.WithCancel(ctx)
	held := startRolloverHeldOwner(heldCtx, cancel, oldTCP, packets, "origin.test", udp.LocalAddr().(*net.UDPAddr).Port, oldPID, old.Instance.PID.Load, func() (string, bool) {
		actual := ""
		if p := persistentUDPPeer.Load(); p != nil {
			actual = p.(string)
		}
		return actual, changedUDPPeer.Load()
	}, time.Second, diagnostic)
	defer func() { stopHeld(); oldTCP.Close(); packets.Close(); <-held.Done }()
	checkHeld := func() {
		t.Helper()
		select {
		case e := <-held.Errors:
			if held.DiagnosticFailed.Load() {
				t.Log("held diagnostics unavailable or dump exceeded 200ms; original failure follows")
			}
			t.Fatal(e)
		default:
		}
	}
	select {
	case <-held.Ready:
	case <-ctx.Done():
		checkHeld()
		t.Fatal(ctx.Err())
	case <-time.After(10 * time.Second):
		checkHeld()
		t.Fatal("held client failed to start")
	}
	heldSince := time.Now()
	lastCheckpoint := time.Time{}
	rotations := 0
	checkpoint := func(final bool) {
		checkHeld()
		if old.Instance.PID.Load() != oldPID {
			t.Fatal("held engine process changed")
		}
		if !final && time.Since(lastCheckpoint) < time.Minute {
			return
		}
		lastCheckpoint = time.Now()
		r.mu.RLock()
		engines := len(r.entries)
		references := int64(0)
		for _, entry := range r.entries {
			references += entry.route.References.Load()
		}
		r.mu.RUnlock()
		snapshot := held.Snapshot.Load()
		writeRolloverSoakCheckpoint(t, rolloverSoakCheckpoint{SourceSHA: os.Getenv("ROLLOVER_SOURCE_SHA"), CoreSHA: hex.EncodeToString(digest[:]), CoreVersion: version, HeldSince: heldSince, At: time.Now(), Seconds: time.Since(heldSince).Seconds(), TCPSequence: snapshot.TCPSequence, UDPSequence: snapshot.UDPSequence, OldPID: snapshot.OldPID, UDPOriginPeer: snapshot.UDPOriginPeer, LastKeepalive: snapshot.At, OldActive: old.Active.Load(), Engines: engines, References: references, Rotations: rotations, Final: final, DR1Passed: final && soak >= 24*time.Hour && time.Since(heldSince) >= 24*time.Hour})
		checkHeld()
	}
	rotate := func(index int) {
		t.Helper()
		nextConfig := makeConfig(index)
		if os.Getenv("ROLLOVER_NATIVE_BULK") == "1" {
			nextConfig.Policies[0].DisableSpeedProbes = false
			nextConfig.Policies[0].SpeedURL = target + "/bulk"
			nextConfig.Policies[0].ProbeBudgetBytes = 128 << 20
		}
		previousConfig := c
		a.mu.Lock()
		if a.state.Config != nil {
			previousConfig = *a.state.Config
		}
		a.mu.Unlock()
		nextConfig.Routes[0].Previous = []config.RevisionPermit{{Fingerprint: c.Routes[0].Fingerprint, ValidUntil: c.Routes[0].ValidUntil}}
		if previousConfig.Routes[0].Fingerprint != c.Routes[0].Fingerprint {
			nextConfig.Routes[0].Previous = append(nextConfig.Routes[0].Previous, config.RevisionPermit{Fingerprint: previousConfig.Routes[0].Fingerprint, ValidUntil: previousConfig.Routes[0].ValidUntil})
		}
		r.Restrict(nextConfig)
		if err = r.Apply(ctx, nextConfig, g); err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		a.state.Config = &nextConfig
		a.state.Generation = nextConfig.Generation
		a.mu.Unlock()
		admissions := 0
		started := time.Now()
		for {
			if time.Since(started) > 210*time.Second {
				t.Fatal("one rotation exceeded bounded native deadline")
			}
			select {
			case <-ctx.Done():
				checkHeld()
				t.Fatal("canary deadline", ctx.Err())
			default:
			}
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
			checkHeld()
			checkpoint(false)
			if state == "active_draining" {
				break
			}
			conn, route, e := g.dial(ctx, "policy:p", echoTarget)
			if e != nil {
				t.Fatal(e)
			}
			assertEcho(conn)
			conn.Close()
			if route.Config.Fingerprint == nextConfig.Routes[0].Fingerprint {
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
			if route.Config.Fingerprint != nextConfig.Routes[0].Fingerprint {
				t.Fatal("old revision still admits after promotion")
			}
			assertEcho(conn)
			conn.Close()
		}
		checkHeld()
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
		rotations++
		t.Logf("native wall-clock rollover: %d successful new admissions in %s; old TCP and UDP continuous", admissions, time.Since(started))
	}
	rotate(1)
	for soak > 0 && time.Since(heldSince) < soak {
		if ctx.Err() != nil {
			t.Fatal("soak deadline", ctx.Err())
		}
		checkHeld()
		checkpoint(false)
		if rotations < 4 && time.Since(heldSince) >= time.Duration(rotations)*soak/4 {
			rotate(rotations + 1)
		}
		select {
		case <-ctx.Done():
			checkHeld()
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	checkHeld()
	checkpoint(true)
}
