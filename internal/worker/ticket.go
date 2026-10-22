package worker

import (
	"encoding/json"
	"fmt"
	"net"
	"proxyplatform/internal/config"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type routeTicket struct {
	scope   *listenerScope
	runtime *Runtime
	route   *Route
	policy  string
	once    sync.Once
	result  atomic.Bool
}

func baseFingerprint(v config.Route) string {
	if v.Fingerprint != "" {
		return v.Fingerprint
	}
	return fingerprint(v)
}

func (r *Runtime) overlapDeadlineLocked(v config.Route) time.Time {
	if r.constraints != nil {
		for _, next := range r.constraints.Routes {
			if next.ID == v.ID {
				if fingerprint(next) == fingerprint(v) {
					return next.ValidUntil
				}
				for _, permit := range next.Previous {
					if permit.Fingerprint == baseFingerprint(v) {
						return permit.ValidUntil
					}
				}
			}
		}
	}
	return v.ValidUntil
}

// overlapAllowedLocked is bounded by both the current policy membership and
// the frozen authorization of the superseded revision. It never restores a
// removed route, changed egress binding or credential after a restrictive edit.
func (r *Runtime) overlapAllowedLocked(policy string, v config.Route) bool {
	if r.constraints == nil {
		return false
	}
	for _, next := range r.constraints.Routes {
		if next.ID != v.ID || next.Core != v.Core || next.EgressID != v.EgressID {
			continue
		}
		if config.ServerPolicyDigest(next.CheckServer, next.AllowPrivate, next.ServerAllowCIDRs) != config.ServerPolicyDigest(v.CheckServer, v.AllowPrivate, v.ServerAllowCIDRs) {
			return false
		}
		if policy != "" && r.allowed[policy][next.ID] != fingerprint(next) {
			return false
		}
		// A source binding edit must not authorize the retired ISP socket.
		if path, ok := r.constraints.Egress(v.EgressID); ok {
			var outbound map[string]any
			if json.Unmarshal(v.Outbound, &outbound) != nil || outbound["sendThrough"] != net.ParseIP(path.SourceIP).String() || !path.Enabled {
				return false
			}
		}
		if fingerprint(next) == fingerprint(v) {
			return true
		}
		for _, permit := range next.Previous {
			if permit.Fingerprint == baseFingerprint(v) && time.Now().Before(permit.ValidUntil) && time.Now().Before(next.ValidUntil) {
				return true
			}
		}
	}
	return false
}

// Reserve before entering a dial, while GC and admission view publication are
// excluded by the same lock. References cover setup, established I/O and close.
func (r *Runtime) reserveRoute(key string, v *Route) (*routeTicket, error) {
	return r.reserveRouteCapability(key, v, false)
}

func udpProven(v *Route) bool {
	return udpProvenAt(v, time.Now())
}

func udpProvenAt(v *Route, now time.Time) bool {
	age := now.Unix() - v.UDPChecked.Load()
	return !v.Config.CheckServer || (v.UDPHealthy.Load() && v.UDPChecks.Load() >= 3 && age >= 0 && age <= 90)
}

func (r *Runtime) reserveRouteCapability(key string, v *Route, udp bool) (*routeTicket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	policy := ""
	if strings.HasPrefix(key, "policy:") {
		policy = strings.TrimPrefix(key, "policy:")
	}
	v = r.chooseRevisionLocked(v, time.Now())
	if r.establishedOnlyLocked(v.Config) {
		return nil, fmt.Errorf("revision_draining")
	}
	if v.Config.CheckServer && len(v.Config.Previous) > 0 && r.rollovers[v.Config.ID] == nil && v.Instance.ProbeSamples.Load() < 3 {
		return nil, fmt.Errorf("revision_protocol_evidence_pending")
	}
	if udp && (!v.Config.UDP || !udpProven(v)) {
		if p := r.rollovers[v.Config.ID]; p != nil && p.old.Config.UDP && udpProven(p.old) {
			v = p.old
		} else {
			return nil, fmt.Errorf("udp_not_validated")
		}
	}
	if r.drained.Load() || time.Now().Unix() > r.lease.Load() || !r.permitted(policy, v.Config) {
		return nil, fmt.Errorf("route_not_admitted")
	}
	found := false
	for _, e := range r.entries {
		if e.route == v {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("route_retired")
	}
	v.References.Add(1)
	return &routeTicket{runtime: r, route: v, policy: policy}, nil
}

func (r *Runtime) establishedOnlyLocked(v config.Route) bool {
	if v.EstablishedOnly {
		return true
	}
	if r.liveGuard != nil && r.liveGuard.Routes[v.Profile()].EstablishedOnly {
		return true
	}
	if r.constraints != nil {
		for _, route := range r.constraints.Routes {
			if route.ID == v.ID {
				return route.EstablishedOnly
			}
		}
	}
	return false
}

func (t *routeTicket) allowed() bool {
	r := t.runtime
	if r.drained.Load() || time.Now().Unix() > r.lease.Load() {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.permitted(t.policy, t.route.Config) && r.sessionScopeAllowedLocked(t.scope)
}

func (r *Runtime) canReuse(t *routeTicket) bool {
	if t == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e := r.current[t.route.Config.ID]
	return e != nil && e.route == t.route && r.permitted(t.policy, t.route.Config)
}

func (t *routeTicket) release() {
	if t != nil {
		t.once.Do(func() { t.route.References.Add(-1) })
	}
}

func (t *routeTicket) outcome(ok bool) {
	if t == nil || !t.result.CompareAndSwap(false, true) {
		return
	}
	r := t.runtime
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.rollovers[t.route.Config.ID]; p != nil && p.next == t.route {
		if ok {
			p.success++
		} else {
			p.failed++
		}
	}
}
