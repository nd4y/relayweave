package worker

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"proxyplatform/internal/core"
	"strconv"
	"time"
)

// A real UDP request/response, independent of the local SOCKS control socket.
// Reserved bytes include both capped responses and request/encapsulation cost.
func dnsUDPCheck(ctx context.Context, v *Route, target string) bool {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return false
	}
	session, err := v.Instance.Adapter().OpenUDP(ctx, v.Config.Endpoint, 3*time.Second)
	if err != nil {
		return false
	}
	defer session.Close()
	query := []byte{0, 0, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
	if _, err = rand.Read(query[:2]); err != nil {
		return false
	}
	if err = session.WritePacket(core.Packet{Host: host, Port: port, Payload: query}); err != nil {
		return false
	}
	session.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 512)
	packet, err := session.ReadPacket(buf)
	if err != nil || len(packet.Payload) < 12 {
		return false
	}
	return binary.BigEndian.Uint16(packet.Payload[:2]) == binary.BigEndian.Uint16(query[:2]) && packet.Payload[2]&0x80 != 0 && packet.Payload[2]&0x02 == 0
}

func (a *Agent) probeUDPCapability(ctx context.Context) {
	r := a.runtime
	if !r.applyMu.TryLock() {
		return
	}
	defer r.applyMu.Unlock()
	r.mu.Lock()
	var candidate *Route
	policy := ""
	budget := int64(0)
	now := time.Now()
	if !r.drained.Load() && now.Unix() <= r.lease.Load() {
		for _, p := range r.policies {
			for _, id := range p.config.Routes {
				e := r.current[id]
				if e == nil || !e.route.Config.CheckServer || !e.route.Config.UDP || !e.route.Instance.Ready.Load() || !r.permitted(p.config.ID, e.route.Config) || now.Unix()-e.route.UDPAttempt.Load() < 30 {
					continue
				}
				if candidate == nil || e.route.UDPAttempt.Load() < candidate.UDPAttempt.Load() {
					candidate = e.route
					policy = p.config.ID
					budget = p.config.ProbeBudgetBytes
				}
			}
		}
	}
	if candidate != nil {
		candidate.References.Add(1)
		candidate.UDPAttempt.Store(now.Unix())
	}
	r.mu.Unlock()
	if candidate == nil {
		return
	}
	defer candidate.References.Add(-1)
	if !r.reserveProbe(now, policy, 2048, budget) {
		return
	}
	a.mu.Lock()
	err := a.saveLocked()
	a.mu.Unlock()
	if err != nil {
		return
	}
	targets := r.Configuration().UDPProbeTargets
	if len(targets) == 0 {
		targets = []string{"1.1.1.1:53", "9.9.9.9:53"}
	}
	ok := false
	for _, target := range targets {
		if dnsUDPCheck(ctx, candidate, target) {
			ok = true
			break
		}
	}
	candidate.UDPHealthy.Store(ok)
	candidate.UDPChecked.Store(now.Unix())
	if ok {
		candidate.UDPChecks.Add(1)
	} else {
		candidate.UDPChecks.Store(0)
	}
}
