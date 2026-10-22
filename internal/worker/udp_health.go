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
	if !a.probeMu.TryLock() {
		return
	}
	defer a.probeMu.Unlock()
	r := a.runtime
	if !r.applyMu.TryLock() {
		return
	}
	r.mu.Lock()
	now := time.Now()
	choice := r.reserveUDPProbeLocked(now)
	r.mu.Unlock()
	r.applyMu.Unlock()
	if choice.route == nil {
		return
	}
	candidate := choice.route
	defer candidate.References.Add(-1)
	if !r.reserveProbe(now, choice.policy, 2048, choice.budget) {
		return
	}
	a.mu.Lock()
	err := a.saveLocked()
	a.mu.Unlock()
	if err != nil {
		return
	}
	r.mu.RLock()
	allowed := ctx.Err() == nil && !r.drained.Load() && time.Now().Unix() <= r.lease.Load() && r.permitted(choice.policy, candidate.Config) && !r.establishedOnlyLocked(candidate.Config)
	cfg := r.config
	if r.constraints != nil {
		cfg = *r.constraints
	}
	targets := append([]string(nil), cfg.UDPProbeTargets...)
	r.mu.RUnlock()
	if !allowed {
		return
	}
	if len(targets) == 0 {
		targets = []string{"1.1.1.1:53", "9.9.9.9:53"}
	}
	ok := false
	probeCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	for _, target := range targets {
		if dnsUDPCheck(probeCtx, candidate, target) {
			ok = true
			break
		}
	}
	r.mu.Lock()
	if !r.drained.Load() && time.Now().Unix() <= r.lease.Load() && r.permitted(choice.policy, candidate.Config) && !r.establishedOnlyLocked(candidate.Config) {
		recordUDPProbe(candidate, ok, time.Now())
	}
	r.mu.Unlock()
}

// A separate serial loop cannot be delayed behind the ten-second capacity
// sampler. Admission and apply locks are released before persistence/network I/O.
func (a *Agent) udpProbes() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		a.probeUDPCapability(a.ctx)
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type udpProbeChoice struct {
	route  *Route
	policy string
	budget int64
}

// Call with r.mu held. Refresh admitted routes at 45s (90s admission TTL),
// otherwise finish one three-sample warmup before spreading to cold routes.
// Failed routes back off30s; successful warmup samples are spaced at least1s.
func (r *Runtime) reserveUDPProbeLocked(now time.Time) udpProbeChoice {
	if r.drained.Load() || now.Unix() > r.lease.Load() {
		return udpProbeChoice{}
	}
	choices := map[*Route]udpProbeChoice{}
	add := func(v *Route, p *policyState) {
		if !v.Config.CheckServer || !v.Config.UDP || !v.Instance.Ready.Load() || r.establishedOnlyLocked(v.Config) || !r.permitted(p.config.ID, v.Config) {
			return
		}
		interval := int64(1)
		if udpProvenAt(v, now) {
			interval = 45
		} else if v.UDPChecked.Load() != 0 && !v.UDPHealthy.Load() {
			interval = 30
		}
		if v.UDPAttempt.Load() != 0 && now.Unix()-v.UDPAttempt.Load() < interval {
			return
		}
		if prior, ok := choices[v]; !ok || p.config.ProbeBudgetBytes > prior.budget || (p.config.ProbeBudgetBytes == prior.budget && p.config.ID < prior.policy) {
			choices[v] = udpProbeChoice{v, p.config.ID, p.config.ProbeBudgetBytes}
		}
	}
	for _, p := range r.policies {
		for _, id := range p.config.Routes {
			if e := r.current[id]; e != nil {
				add(e.route, p)
			}
			if plan := r.rollovers[id]; plan != nil && plan.state != "completed" && (plan.state != "active_draining" || !udpProvenAt(plan.next, now)) {
				add(plan.old, p)
			}
		}
	}
	var best udpProbeChoice
	better := func(a, b *Route) bool {
		ap, bp := udpProvenAt(a, now), udpProvenAt(b, now)
		if ap != bp {
			return ap
		} // Protect fresh admissions before more exploration.
		if ap && a.UDPChecked.Load() != b.UDPChecked.Load() {
			return a.UDPChecked.Load() < b.UDPChecked.Load()
		}
		ac, bc := a.UDPChecks.Load(), b.UDPChecks.Load()
		if now.Unix()-a.UDPChecked.Load() > 90 {
			ac = 0
		}
		if now.Unix()-b.UDPChecked.Load() > 90 {
			bc = 0
		}
		if ac != bc {
			return ac > bc
		}
		if a.UDPAttempt.Load() != b.UDPAttempt.Load() {
			return a.UDPAttempt.Load() < b.UDPAttempt.Load()
		}
		if a.Config.ID != b.Config.ID {
			return a.Config.ID < b.Config.ID
		}
		return a.Config.Endpoint < b.Config.Endpoint
	}
	for _, c := range choices {
		if best.route == nil || better(c.route, best.route) {
			best = c
		}
	}
	if best.route != nil {
		best.route.References.Add(1)
		best.route.UDPAttempt.Store(now.Unix())
	}
	return best
}

func recordUDPProbe(v *Route, ok bool, now time.Time) {
	if !ok || now.Unix()-v.UDPChecked.Load() > 90 {
		v.UDPChecks.Store(0)
	}
	v.UDPHealthy.Store(ok)
	v.UDPChecked.Store(now.Unix())
	if ok {
		v.UDPChecks.Add(1)
	}
}
