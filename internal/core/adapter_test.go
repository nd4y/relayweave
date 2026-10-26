package core

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/socks"
)

func TestUnknownEngineFailsBeforeExecution(t *testing.T) {
	if _, err := New(config.Config{Cores: []config.Core{{Family: "unregistered", Binary: "/not/executed"}}}); err == nil {
		t.Fatal("unknown adapter accepted")
	}
}

func TestXrayUDPPrivateEndpointAndPacketBoundary(t *testing.T) {
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		c, e := listener.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(3 * time.Second))
		b := make([]byte, 3)
		if _, e = io.ReadFull(c, b); e != nil {
			return
		}
		socks.Write(c, []byte{5, 0})
		if _, e = io.ReadFull(c, b); e != nil {
			return
		}
		socks.ReadAddress(c)
		socks.Reply(c, 0, "127.0.0.1", udp.LocalAddr().(*net.UDPAddr).Port)
		io.Copy(io.Discard, c)
	}()
	go func() {
		buffer := make([]byte, 4096)
		n, from, e := udp.ReadFromUDP(buffer)
		if e == nil {
			udp.WriteToUDP(buffer[:n], from)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := (XrayAdapter{}).OpenUDP(ctx, listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err = session.WritePacket(Packet{Host: "target.invalid", Port: 5353, Payload: []byte("opaque-payload")}); err != nil {
		t.Fatal(err)
	}
	session.SetReadDeadline(time.Now().Add(time.Second))
	packet, err := session.ReadPacket(make([]byte, 4096))
	if err != nil || packet.Host != "target.invalid" || packet.Port != 5353 || string(packet.Payload) != "opaque-payload" {
		t.Fatalf("packet contract: %+v %v", packet, err)
	}
	cancel()
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel did not close packet session")
	}
}
