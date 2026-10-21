package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxyplatform/internal/config"
)

// The receiver reports actual socket peers, not configuration intent. Run with
// each approved Xray version in Docker; normal unit runs skip this opt-in test.
func TestXrayEgressSourceTCPUDP(t *testing.T) {
	binary := os.Getenv("XRAY_EGRESS_TEST_BIN")
	if binary == "" {
		t.Skip("set XRAY_EGRESS_TEST_BIN for real core tests")
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	go func() {
		for {
			c, e := tcp.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				ip, _, _ := net.SplitHostPort(c.RemoteAddr().String())
				fmt.Fprintln(c, ip)
			}()
		}
	}()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	routes := []config.Route{}
	for i, ip := range []string{"127.0.0.2", "127.0.0.3", "192.0.2.99"} {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		endpoint := l.Addr().String()
		l.Close()
		out, _ := json.Marshal(map[string]any{"protocol": "freedom", "sendThrough": ip, "settings": map[string]any{}})
		routes = append(routes, config.Route{ID: fmt.Sprintf("lane%d", i), Endpoint: endpoint, Outbound: out})
	}
	bytes, err := Render(routes)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "xray.json")
	if err = os.WriteFile(path, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	test := exec.CommandContext(ctx, binary, "run", "-test", "-config", path)
	if out, e := test.CombinedOutput(); e != nil {
		t.Fatalf("core config rejected: %s %v", out, e)
	}
	cmd := exec.CommandContext(ctx, binary, "run", "-config", path)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); cmd.Wait() }()
	for _, r := range routes {
		deadline := time.Now().Add(5 * time.Second)
		for {
			c, e := net.DialTimeout("tcp", r.Endpoint, 100*time.Millisecond)
			if e == nil {
				c.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("core listener unavailable")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	adapter := XrayAdapter{}
	for i, r := range routes[:2] {
		expected := fmt.Sprintf("127.0.0.%d", i+2)
		c, e := adapter.OpenTCP(ctx, r.Endpoint, tcp.Addr().String(), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		c.SetDeadline(time.Now().Add(time.Second))
		reply, e := bufio.NewReader(c).ReadBytes('\n')
		c.Close()
		if e != nil || strings.TrimSpace(string(reply)) != expected {
			t.Fatalf("TCP source not enforced: %s %v", reply, e)
		}
		session, e := adapter.OpenUDP(ctx, r.Endpoint, time.Second)
		if e != nil {
			t.Fatal(e)
		}
		session.SetReadDeadline(time.Now().Add(2 * time.Second))
		e = session.WritePacket(Packet{Host: "127.0.0.1", Port: udp.LocalAddr().(*net.UDPAddr).Port, Payload: []byte("probe")})
		if e != nil {
			t.Fatal(e)
		}
		packet, e := session.ReadPacket(make([]byte, 4096))
		session.Close()
		if e != nil || string(packet.Payload) != expected {
			t.Fatalf("UDP source not enforced: %s %v", packet.Payload, e)
		}
	}
	c, e := adapter.OpenTCP(ctx, routes[2].Endpoint, tcp.Addr().String(), time.Second)
	if e == nil {
		c.SetDeadline(time.Now().Add(time.Second))
		reply, _ := io.ReadAll(c)
		c.Close()
		if len(reply) > 0 {
			t.Fatal("unassigned source silently fell back to another IP")
		}
	}
	session, e := adapter.OpenUDP(ctx, routes[2].Endpoint, time.Second)
	if e == nil {
		defer session.Close()
		session.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if e = session.WritePacket(Packet{Host: "127.0.0.1", Port: udp.LocalAddr().(*net.UDPAddr).Port, Payload: []byte("invalid-source")}); e == nil {
			if packet, e := session.ReadPacket(make([]byte, 4096)); e == nil || len(packet.Payload) > 0 {
				t.Fatal("UDP unassigned source fell back")
			}
		}
	}
}
