package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/socks"
)

func udpRelayAddress(cfg config.UDP, local net.Addr) (string, string, error) {
	if cfg.Advertise != "auto" {
		return cfg.Bind, cfg.Advertise, nil
	}
	tcp, ok := local.(*net.TCPAddr)
	if !ok || tcp.IP == nil || tcp.IP.IsUnspecified() {
		return "", "", fmt.Errorf("udp auto requires a concrete listener address")
	}
	return tcp.IP.String(), tcp.IP.String(), nil
}
func (g *Gateway) allocateUDP() (*net.UDPConn, int, error) {
	return g.allocateUDPOn(g.configuration().UDP.Bind)
}
func (g *Gateway) allocateUDPOn(bind string) (*net.UDPConn, int, error) {
	g.portMu.Lock()
	defer g.portMu.Unlock()
	cfg := g.configuration()
	for p := cfg.UDP.PortMin; p <= cfg.UDP.PortMax; p++ {
		if g.ports[p] {
			continue
		}
		s, e := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(bind), Port: p})
		if e != nil {
			continue
		}
		g.ports[p] = true
		return s, p, nil
	}
	return nil, 0, &net.OpError{Op: "listen", Net: "udp", Err: errPorts{}}
}

type errPorts struct{}

func (errPorts) Error() string    { return "UDP relay port budget exhausted" }
func (g *Gateway) freePort(p int) { g.portMu.Lock(); delete(g.ports, p); g.portMu.Unlock() }
func (g *Gateway) udpAssociate(l config.Listener, client net.Conn, requestedHost string, requestedPort int) {
	cfg := g.configuration()
	peer := client.RemoteAddr().(*net.TCPAddr).IP
	if requestedHost != "0.0.0.0" && requestedHost != "::" {
		ip := net.ParseIP(requestedHost)
		if ip == nil || !ip.Equal(peer) {
			socks.Reply(client, 2, "0.0.0.0", 0)
			return
		}
	}
	var up core.PacketSession
	var route *Route
	var ticket *routeTicket
	if g.Dynamic != nil {
		release, err := g.Dynamic.AdmitPolicy(routeKey(l))
		if err != nil {
			socks.Reply(client, 4, "0.0.0.0", 0)
			return
		}
		defer release()
	}
	candidates, decision := g.topologyCandidates(routeKey(l), l.ID, "socks5-udp")
	for _, r := range candidates {
		if g.Dynamic != nil && !r.Config.UDP {
			continue
		}
		if g.Dynamic != nil {
			var err error
			ticket, err = g.Dynamic.reserveRouteCapability(routeKey(l), r, true)
			if err != nil {
				continue
			}
			r = ticket.route
			ticket.scope = &listenerScope{Listener: l, Peer: client.RemoteAddr().String()}
		}
		started := time.Now()
		c, e := r.Instance.Adapter().OpenUDP(g.ctx, r.Config.Endpoint, cfg.Timeout())
		if e == nil {
			up, route = c, r
			r.success(time.Since(started))
			break
		}
		ticket.outcome(false)
		ticket.release()
		ticket = nil
		r.Failures.Add(1)
		r.failed()
		g.topologyRouteResult(decision, r, "target_connect_failed")
	}
	if up == nil {
		socks.Reply(client, 4, "0.0.0.0", 0)
		return
	}
	defer up.Close()
	defer ticket.release()
	if ticket != nil && !ticket.allowed() {
		socks.Reply(client, 4, "0.0.0.0", 0)
		return
	}
	bind, advertise, e := udpRelayAddress(cfg.UDP, client.LocalAddr())
	if e != nil {
		socks.Reply(client, 1, "0.0.0.0", 0)
		return
	}
	down, port, e := g.allocateUDPOn(bind)
	if e != nil {
		g.udpDrops.Add(1)
		socks.Reply(client, 1, "0.0.0.0", 0)
		return
	}
	defer down.Close()
	defer g.freePort(port)
	ctx, cancel := context.WithCancel(g.ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { down.Close(); up.Close(); client.Close() })
	defer stop()
	if socks.Reply(client, 0, advertise, port) != nil {
		return
	}
	g.udpActive.Add(1)
	g.topologyRouteResult(decision, route, "selected_by_policy")
	defer g.udpActive.Add(-1)
	route.Connections.Add(1)
	route.Active.Add(1)
	defer route.Active.Add(-1)
	var peerMu sync.Mutex
	var destination *net.UDPAddr
	activityBase := time.Now()
	var lastActivity atomic.Int64
	idle := time.Duration(cfg.UDP.IdleSeconds) * time.Second
	expired := func() bool { return time.Since(activityBase)-time.Duration(lastActivity.Load()) >= idle }
	touch := func() { lastActivity.Store(time.Since(activityBase).Nanoseconds()) }
	flows := map[[32]byte]core.Packet{}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); io.Copy(io.Discard, client); cancel() }()
	go func() {
		defer wg.Done()
		select {
		case <-up.Done():
		case <-ctx.Done():
		}
		cancel()
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, 65535)
		frame := make([]byte, cfg.UDP.MaxDatagramBytes)
		for {
			if ticket != nil && !ticket.allowed() {
				return
			}
			up.SetReadDeadline(time.Now().Add(min(time.Second, idle)))
			packet, e := up.ReadPacket(buf)
			if e != nil {
				if isTimeout(e) && !expired() {
					continue
				}
				return
			}
			ticket.outcome(true)
			address, err := socks.Address(packet.Host, packet.Port)
			n := 3 + len(address) + len(packet.Payload)
			if err != nil || n > cfg.UDP.MaxDatagramBytes {
				g.udpDrops.Add(1)
				continue
			}
			copy(frame[3:], address)
			copy(frame[3+len(address):], packet.Payload)
			peerMu.Lock()
			target := destination
			peerMu.Unlock()
			if target != nil {
				if _, e = down.WriteToUDP(frame[:n], target); e != nil {
					return
				}
				route.BytesDown.Add(int64(len(packet.Payload)))
				g.udpPackets.Add(1)
				touch()
			}
		}
	}()
	buf := make([]byte, 65535)
	for {
		if ticket != nil && !ticket.allowed() {
			break
		}
		down.SetReadDeadline(time.Now().Add(min(time.Second, idle)))
		n, from, e := down.ReadFromUDP(buf)
		if e != nil {
			if isTimeout(e) && !expired() {
				continue
			}
			break
		}
		if !from.IP.Equal(peer) || n > cfg.UDP.MaxDatagramBytes || socks.ValidateDatagram(buf[:n]) != nil {
			g.udpDrops.Add(1)
			continue
		}
		peerMu.Lock()
		valid := true
		if destination == nil {
			if requestedPort != 0 && from.Port != requestedPort {
				valid = false
			} else {
				destination = from
			}
		} else if from.Port != destination.Port || !from.IP.Equal(destination.IP) {
			valid = false
		}
		peerMu.Unlock()
		if !valid {
			g.udpDrops.Add(1)
			continue
		}
		headerSize := n - payloadSize(buf[:n])
		flow := sha256.Sum256(buf[3:headerSize])
		packet, known := flows[flow]
		if !known {
			host, port, _, err := socks.ReadAddress(bytes.NewReader(buf[3:n]))
			if err != nil || forbiddenTarget(net.JoinHostPort(host, strconv.Itoa(port))) || len(flows) >= 128 || (ticket != nil && !ticket.allowed()) {
				g.udpDrops.Add(1)
				continue
			}
			packet = core.Packet{Host: host, Port: port}
			flows[flow] = packet
		}
		packet.Payload = buf[headerSize:n]
		if e = up.WritePacket(packet); e != nil {
			break
		}
		route.BytesUp.Add(int64(payloadSize(buf[:n])))
		g.udpPackets.Add(1)
		touch()
	}
	cancel()
	wg.Wait()
}
func payloadSize(frame []byte) int {
	header := 0
	switch frame[3] {
	case 1:
		header = 10
	case 4:
		header = 22
	case 3:
		header = 7 + int(frame[4])
	}
	return max(0, len(frame)-header)
}
