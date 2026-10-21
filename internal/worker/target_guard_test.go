package worker

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/socks"
)

func TestMetadataDestinationsDeniedBeforeCoreAccess(t *testing.T) {
	for _, target := range []string{
		"169.254.169.254:80", "100.100.100.200:80",
		"[fd00:ec2::254]:80", "[FD00:0EC2:0:0:0:0:0:0254]:80",
		"[fd00:ec2::254%eth0]:80", "[::ffff:169.254.169.254]:80",
		"[fe80::1%eth0]:80", "[::1%lo]:80",
	} {
		// No manager/adapter exists: reaching a core would panic or dial.
		g := &Gateway{}
		connection, _, err := g.dial(context.Background(), "", target)
		if connection != nil || err == nil || err.Error() != "destination_denied" {
			t.Fatalf("metadata destination was not rejected before core access: %q", target)
		}
	}
	for _, target := range []string{"origin:9000", "[fd12:3456::2]:9000", "[2001:db8::2]:443", "10.0.0.2:443"} {
		if forbiddenTarget(target) {
			t.Fatalf("ordinary configured destination blocked: %q", target)
		}
	}
}

func TestSOCKSFailureRequestsHealthWithoutQuarantiningDestinationFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan error, 1)
	go func() {
		peer, e := listener.Accept()
		if e != nil {
			result <- e
			return
		}
		defer peer.Close()
		peer.SetDeadline(time.Now().Add(3 * time.Second))
		_, e = io.ReadFull(peer, make([]byte, 3))
		if e == nil {
			e = socks.Write(peer, []byte{5, 0})
		}
		if e == nil {
			_, e = io.ReadFull(peer, make([]byte, 3))
		}
		if e == nil {
			_, _, _, e = socks.ReadAddress(peer)
		}
		if e == nil {
			e = socks.Reply(peer, 5, "0.0.0.0", 0)
		}
		result <- e
	}()
	instance := &core.Instance{}
	instance.Ready.Store(true)
	route := &Route{Config: config.Route{ID: "fixture", Endpoint: listener.Addr().String()}, Instance: instance}
	gateway := &Gateway{Config: config.Config{ConnectTimeoutMS: 1000}, Routes: []*Route{route}}
	connection, _, err := gateway.dial(context.Background(), "", "origin:9000")
	if connection != nil || err == nil {
		t.Fatal("SOCKS refusal unexpectedly succeeded")
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	if instance.HealthRequests.Load() != 1 {
		t.Fatal("failed core handshake did not request independent health confirmation")
	}
	if !instance.Ready.Load() || route.BlockedUntil.Load() != 0 {
		t.Fatal("a destination refusal quarantined the proxy without health confirmation")
	}
	if !gateway.targetCooling("origin:9000", route.Config.ID) {
		t.Fatal("destination-specific cooldown was lost")
	}
}
