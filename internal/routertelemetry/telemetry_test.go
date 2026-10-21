package routertelemetry

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWireBoundsAndEncoding(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 16383, 16384, 65536} {
		var b bytes.Buffer
		word := strings.Repeat("x", n)
		if e := writeWord(&b, word); e != nil {
			t.Fatal(e)
		}
		b.WriteByte(0)
		words, e := readSentence(bufio.NewReader(&b))
		if e != nil {
			t.Fatal(e)
		}
		if n > 0 && (len(words) != 1 || words[0] != word) {
			t.Fatal("wire round trip")
		}
	}
	for _, b := range [][]byte{{0xf0, 0xff, 0xff, 0xff, 0xff}, {0xf8}, {0xe0, 0x10, 0, 0}} {
		if _, e := readSentence(bufio.NewReader(bytes.NewReader(b))); e == nil {
			t.Fatal("unbounded frame accepted")
		}
	}
	var b bytes.Buffer
	for i := 0; i < 65; i++ {
		_ = writeWord(&b, "a")
	}
	b.WriteByte(0)
	if _, e := readSentence(bufio.NewReader(&b)); e == nil {
		t.Fatal("word budget not enforced")
	}
}

func sample(now time.Time, rx, tx uint64) Snapshot {
	return Snapshot{ObservedAt: now, Reading: Reading{WANs: []WAN{{ID: "wan", ISP: "isp2", Interface: "pppoe", Running: true, RXBytes: rx, TXBytes: tx}}}}
}
func TestRateResetGapAndLinkDown(t *testing.T) {
	now := time.Now()
	p := sample(now, 100, 200)
	s := sample(now.Add(2*time.Second), 500, 300)
	applyRates(&s, &p, 10*time.Second)
	if s.WANs[0].Quality != "measured" || *s.WANs[0].DownloadBPS != 200 || *s.WANs[0].UploadBPS != 50 {
		t.Fatal("wrong rate/direction")
	}
	s = sample(now.Add(2*time.Second), 1, 300)
	applyRates(&s, &p, 10*time.Second)
	if s.WANs[0].Quality != "counter_reset" || s.WANs[0].DownloadBPS != nil {
		t.Fatal("reset treated as huge rate")
	}
	s = sample(now.Add(20*time.Second), 500, 300)
	applyRates(&s, &p, 10*time.Second)
	if s.WANs[0].DownloadBPS != nil {
		t.Fatal("stale interval used")
	}
	s = sample(now.Add(2*time.Second), 500, 300)
	s.WANs[0].Disabled = true
	applyRates(&s, &p, 10*time.Second)
	if s.WANs[0].Quality != "link_down" || s.WANs[0].DownloadBPS != nil {
		t.Fatal("disabled link measured")
	}
}

type countingAdapter struct{ calls *atomic.Int32 }

func (a *countingAdapter) Read(context.Context) (Reading, error) {
	a.calls.Add(1)
	return Reading{WANs: []WAN{}}, nil
}
func (a *countingAdapter) Close() error { return nil }
func TestOptionalDisabledDoesNotConnect(t *testing.T) {
	var calls atomic.Int32
	r := Registry{"fake": {New: func(RouterConfig) (Adapter, error) { calls.Add(100); return &countingAdapter{&calls}, nil }}}
	for _, enabled := range []bool{false, true} {
		cfg := Config{Enabled: enabled, PollSeconds: 2, StaleSeconds: 10, Routers: []RouterConfig{{ID: "home", Driver: "fake", Enabled: false}}}
		m := New(cfg, r)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		m.Run(ctx)
		if calls.Load() != 0 {
			t.Fatal("disabled module connected")
		}
	}
}

func TestStaleFailuresAndAuthenticatedAPI(t *testing.T) {
	cfg := Config{Enabled: true, StaleSeconds: 10, Routers: []RouterConfig{{ID: "home", Driver: "fake", Enabled: true}}}
	m := New(cfg, Registry{"fake": {}})
	s := sample(time.Now().Add(-time.Minute), 100, 200)
	s.RouterID = "home"
	s.Fresh = true
	s.State = "ready"
	m.snapshots["home"] = s
	d := m.Document(time.Now())
	if d.Routers[0].Fresh || len(d.Routers[0].WANs) != 0 {
		t.Fatal("stale data presented as live")
	}
	token := strings.Repeat("secret", 8)
	handler := m.Handler(token)
	for _, tt := range []struct {
		path, method, auth string
		status             int
	}{{"/v1/snapshot", "GET", "", 401}, {"/v1/snapshot", "GET", "Bearer " + token, 200}, {"/readyz", "GET", "Bearer " + token, 503}, {"/metrics", "GET", "Bearer " + token, 200}, {"/v1/snapshot", "POST", "Bearer " + token, 405}, {"/livez", "GET", "", 200}} {
		r := httptest.NewRequest(tt.method, tt.path, nil)
		r.Header.Set("Authorization", tt.auth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Fatalf("%s: %d", tt.path, w.Code)
		}
		if strings.Contains(w.Body.String(), token) {
			t.Fatal("credential leak")
		}
	}
	m.fail("home", time.Now())
	if got := m.Document(time.Now()).Routers[0]; got.State != "unavailable" || got.Fresh || len(got.WANs) != 0 {
		t.Fatal("failed observation retained")
	}
}

func TestConfigRejectsAmbiguityAndUnsafeListen(t *testing.T) {
	cfg := Config{Version: 1, Listen: "127.0.0.1:18530", Routers: []RouterConfig{{ID: "home", Driver: "mikrotik-api-ssl", WANs: []WANConfig{{ID: "wan", ISP: "isp2", Interface: "pppoe"}}}}}
	path := filepath.Join(t.TempDir(), "config.json")
	check := func(wantErr bool) {
		b, _ := json.Marshal(cfg)
		_ = os.WriteFile(path, b, 0600)
		_, e := Load(path, Builtins())
		if (e != nil) != wantErr {
			t.Fatalf("validation: %v", e)
		}
	}
	check(false)
	cfg.Listen = "0.0.0.0:18530"
	check(true)
	cfg.Listen = "127.0.0.1:18530"
	cfg.Routers[0].WANs = append(cfg.Routers[0].WANs, WANConfig{ID: "other", ISP: "isp2", Interface: "ether1"})
	check(true)
}

// The fixture is an actual TLS RouterOS wire peer, with an independently trusted
// CA. No production credentials or public network are involved.
func tlsFixture(t *testing.T) (RouterConfig, *tls.Config, net.Listener, *sync.WaitGroup) {
	t.Helper()
	hs := httptest.NewTLSServer(http.NotFoundHandler())
	cert := hs.Certificate()
	serverTLS := hs.TLS.Clone()
	hs.Close()
	listener, e := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { listener.Close() })
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	pw := filepath.Join(dir, "password")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600)
	_ = os.WriteFile(pw, []byte("fixture-only-password"), 0600)
	cfg := RouterConfig{Endpoint: listener.Addr().String(), ServerName: "example.com", CAFile: ca, Username: "readonly", PasswordFile: pw, WANs: []WANConfig{{ID: "wan", ISP: "isp2", Interface: "pppoe"}}}
	return cfg, serverTLS, listener, &sync.WaitGroup{}
}

func sendSentence(c net.Conn, words ...string) error {
	for _, w := range words {
		if e := writeWord(c, w); e != nil {
			return e
		}
	}
	_, e := c.Write([]byte{0})
	return e
}

func TestMikrotikTLSReadOnlyAndPersistentSession(t *testing.T) {
	cfg, _, listener, wg := tlsFixture(t)
	var commands, logins atomic.Int32
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, e := listener.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(c)
		for {
			words, e := readSentence(r)
			if e != nil {
				return
			}
			if len(words) == 0 {
				return
			}
			commands.Add(1)
			switch words[0] {
			case "/login":
				logins.Add(1)
				_ = sendSentence(c, "!done")
			case "/system/resource/print":
				_ = sendSentence(c, "!re", "=cpu-load=12")
				_ = sendSentence(c, "!done")
			case "/interface/print":
				_ = sendSentence(c, "!re", "=name=pppoe", "=running=true", "=disabled=false", "=rx-byte=12345", "=tx-byte=6789")
				_ = sendSentence(c, "!done")
			default:
				t.Error("unexpected command reached wire")
				return
			}
		}
	}()
	a, e := newMikrotik(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { a.Close(); wg.Wait() }()
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		reading, e := a.Read(ctx)
		cancel()
		if e != nil {
			t.Fatal(e)
		}
		if reading.WANs[0].RXBytes != 12345 || *reading.CPUPercent != 12 {
			t.Fatal("unexpected reading")
		}
	}
	if logins.Load() != 1 || commands.Load() != 5 {
		t.Fatal("session was not reused")
	}
	if _, e := a.(*mikrotik).command([]string{"/system/identity/set", "=name=unchanged"}); e == nil {
		t.Fatal("write command accepted")
	}
	if commands.Load() != 5 {
		t.Fatal("denied command reached wire")
	}
}

func TestMikrotikRejectsHostnameMismatch(t *testing.T) {
	cfg, _, listener, wg := tlsFixture(t)
	cfg.ServerName = "wrong.invalid"
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, e := listener.Accept()
		if e == nil {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(2 * time.Second))
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	a, e := newMikrotik(cfg)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, e = a.Read(ctx); e == nil {
		t.Fatal("TLS hostname not verified")
	}
	a.Close()
	wg.Wait()
}

func TestMikrotikHonorsReadDeadline(t *testing.T) {
	cfg, _, listener, wg := tlsFixture(t)
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, e := listener.Accept()
		if e == nil {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(time.Second))
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	a, e := newMikrotik(cfg)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, e = a.Read(ctx); e == nil {
		t.Fatal("hung router accepted")
	}
	if time.Since(start) > 700*time.Millisecond {
		t.Fatal("read exceeded budget")
	}
	a.Close()
	wg.Wait()
}
