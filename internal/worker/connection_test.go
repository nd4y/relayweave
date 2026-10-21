package worker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"proxyplatform/internal/core"
	"proxyplatform/internal/topology"
	"testing"
	"time"
)

func TestTCPInfoPayloadAccounting(t *testing.T) {
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	go func() {
		c, e := ln.Accept()
		if e == nil {
			defer c.Close()
			io.Copy(c, c)
		}
	}()
	tcp, e := net.Dial("tcp", ln.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &Gateway{ctx: ctx, sockets: map[*routeConn]struct{}{}}
	r := &Route{}
	r.Active.Store(1)
	c := &routeConn{TCPConn: tcp.(*net.TCPConn), route: r, gateway: g}
	c.baseline()
	defer c.Close()
	payload := bytes.Repeat([]byte("payload-"), 8192)
	done := make(chan error, 1)
	go func() { _, e := c.Write(payload); done <- e }()
	got := make([]byte, len(payload))
	if _, e = io.ReadFull(c, got); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	c.sample()
	if !bytes.Equal(got, payload) || r.BytesUp.Load() != int64(len(payload)) || r.BytesDown.Load() != int64(len(payload)) {
		t.Fatalf("TCP payload mismatch up=%d down=%d want=%d", r.BytesUp.Load(), r.BytesDown.Load(), len(payload))
	}
	c.Close()
	c.Close()
	if r.Active.Load() != 0 {
		t.Fatal("socket lifetime counted more than once")
	}
}

func TestTCPInfoFINIsNotResponsePayload(t *testing.T) {
	for _, response := range [][]byte{nil, []byte("x"), bytes.Repeat([]byte("reply"), 1024)} {
		t.Run(fmt.Sprint(len(response)), func(t *testing.T) {
			listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			payload := []byte("probe-payload")
			release := make(chan struct{})
			defer close(release)
			result := make(chan error, 1)
			go func() {
				peer, e := listener.AcceptTCP()
				if e != nil {
					result <- e
					return
				}
				defer peer.Close()
				peer.SetDeadline(time.Now().Add(3 * time.Second))
				if _, e = peer.Write([]byte("handshake")); e == nil {
					_, e = io.ReadFull(peer, make([]byte, len(payload)))
				}
				if e == nil && len(response) > 0 {
					_, e = peer.Write(response)
				}
				if e == nil {
					e = peer.CloseWrite()
				}
				result <- e
				<-release
			}()
			client, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			client.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err = io.ReadFull(client, make([]byte, len("handshake"))); err != nil {
				t.Fatal(err)
			}
			g := &Gateway{Topology: topology.NewObserver(), sockets: map[*routeConn]struct{}{}}
			route := &Route{Instance: &core.Instance{}}
			route.Active.Store(1)
			connection := &routeConn{TCPConn: client, route: route, gateway: g, topologyGeneration: 7, topologyPolicy: "policy", topologyListener: "http", topologyProtocol: "http-connect"}
			connection.baseline()
			defer connection.Close()
			if _, err = connection.Write(payload); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(connection)
			if err != nil || !bytes.Equal(got, response) {
				t.Fatalf("response=%q error=%v", got, err)
			}
			if err = <-result; err != nil {
				t.Fatal(err)
			}
			connection.sample()
			connection.CloseWrite()
			time.Sleep(10 * time.Millisecond)
			connection.sample()
			connection.checkEarlyFailure()
			connection.checkEarlyFailure()
			frame := topology.Frame{}
			g.Topology.Fill(&frame)
			if len(response) == 0 {
				if len(frame.Decisions) != 1 || frame.Decisions[0].Reason != "early_transport_close" || frame.Decisions[0].Generation != 7 || frame.Decisions[0].Selected != "" {
					t.Fatalf("wrong early-close evidence %+v", frame.Decisions)
				}
			} else if len(frame.Decisions) != 0 {
				t.Fatal("response mislabelled as early close")
			}
			if route.BytesUp.Load() != int64(len(payload)) || route.BytesDown.Load() != int64(len(response)) {
				t.Fatalf("FIN counted as payload: sent=%d received=%d, want=%d/%d", route.BytesUp.Load(), route.BytesDown.Load(), len(payload), len(response))
			}
		})
	}
}
