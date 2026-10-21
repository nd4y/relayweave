package worker

import (
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"proxyplatform/internal/topology"
)

func candidateEvidence(v *Route, score *float64, reason string) topology.Candidate {
	return topology.Candidate{Egress: v.Config.EgressID, Route: v.Config.ID, Engine: v.Config.Core, Active: v.Active.Load(), Score: score, Reason: reason, SpeedBPS: v.SpeedBPS.Load(), MeasurementAt: v.LastSpeed.Load()}
}

// Called inside the selector lock before returning. Scores are only reported
// for the actual pair compared above, never reconstructed from later metrics.
func (r *Runtime) captureCandidatesLocked(p *policyState, result []*Route, d *topology.Decision, now time.Time) {
	if p != nil {
		d.Mode = p.config.Mode
		if d.Method != "deterministic_two_choice" && d.Method != "isp_weighted_path_choice" && d.Method != "automatic_profile_weights" {
			d.Method = p.config.Mode
		}
	}
	seen := map[string]bool{}
	for _, c := range d.Candidates {
		seen[c.Route] = true
	}
	add := func(v *Route, reason string) {
		if seen[v.Config.ID] {
			return
		}
		if len(d.Candidates) >= 32 {
			d.Truncated = true
			return
		}
		seen[v.Config.ID] = true
		d.Candidates = append(d.Candidates, candidateEvidence(v, nil, reason))
	}
	for _, v := range result {
		add(v, "eligible_retry_order")
	}
	if p != nil {
		// Rejections were captured at the native eligibility filter. Do not infer
		// selection-time causes by reading health again after the choice.
		for _, v := range p.best {
			reason := "not_returned_by_selector"
			if p.config.Mode == "manual" && v.Config.ID != p.config.Routes[0] {
				reason = "excluded_by_manual_pin"
			}
			add(v, reason)
		}
		for id, e := range r.current {
			if _, ok := p.rank[id]; ok {
				add(e.route, "not_in_prepared_window")
			}
		}
	}
	routes := []*Route{}
	for _, c := range d.Candidates {
		if e := r.current[c.Route]; e != nil {
			routes = append(routes, e.route)
		}
	}
	groups := r.endpointGroupsLocked(routes, now)
	groupByID := map[string]string{}
	for i, v := range routes {
		groupByID[v.Config.ID] = groups[i]
	}
	for i := range d.Candidates {
		d.Candidates[i].Group = groupByID[d.Candidates[i].Route]
		if entry := r.current[d.Candidates[i].Route]; entry != nil {
			d.Candidates[i].Engine = entry.key
		}
		if p != nil {
			if factor := p.topologyFactors[d.Candidates[i].Route]; factor != "" {
				d.Candidates[i].Factors = append(d.Candidates[i].Factors, factor)
			}
		}
	}
	if p != nil {
		d.PreparedAt = p.topologyPrepared
		for i := range d.Candidates {
			for n, v := range p.best {
				if v.Config.ID == d.Candidates[i].Route {
					role := "prepared_active_path"
					if n >= p.config.ActivePaths {
						role = "warm_reserve"
					}
					d.Candidates[i].Factors = append(d.Candidates[i].Factors, role)
					break
				}
			}
		}
	}
	if len(result) == 0 && d.Reason == "selected_by_policy" {
		d.Reason = "insufficient_observation"
	}
}
func (g *Gateway) topologyCandidates(key, listener, protocol string) ([]*Route, *topology.Decision) {
	if g.Topology == nil || !g.Topology.Begin() {
		return g.candidates(key), nil
	}
	d := &topology.Decision{At: time.Now().UTC(), FlowID: g.Topology.FlowID(), Listener: listener, Protocol: protocol, Reason: "selected_by_policy"}
	if g.Dynamic != nil {
		return g.Dynamic.candidatesObserved(key, d), d
	}
	list := g.candidates(key)
	d.Policy = key
	d.Method = "static_round_robin"
	for _, v := range list {
		if len(d.Candidates) >= 32 {
			d.Truncated = true
			break
		}
		d.Candidates = append(d.Candidates, candidateEvidence(v, nil, "eligible_retry_order"))
	}
	return list, d
}
func (g *Gateway) topologyResult(d *topology.Decision, selected, reason string) {
	if d == nil {
		if reason == "selected_by_policy" || g.Topology == nil {
			return
		}
		d = &topology.Decision{Generation: g.configuration().Generation, Policy: "unknown", Listener: "unknown", Protocol: "unknown", Method: "unsampled_transport_error", Truncated: true}
	}
	d.Selected = selected
	d.Attempted = ""
	if reason != "selected_by_policy" {
		d.Selected = ""
		d.Attempted = selected
	}
	for _, c := range d.Candidates {
		if c.Route == selected {
			d.Engine = c.Engine
			break
		}
	}
	d.Reason = reason
	g.Topology.Offer(*d)
}

var topologySequence atomic.Uint64

// Immutable allowlisted projection; raw Outbound and listener credentials never
// cross this boundary. Uses the selector's existing DNS cache and group function.
func (r *Runtime) TopologyFrame(g *Gateway) *topology.Frame {
	if g.Topology == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	f := &topology.Frame{Worker: os.Getenv("WORKER_ID"), Sequence: topologySequence.Add(1), At: time.Now().UTC(), Generation: r.config.Generation, CatalogSize: len(r.config.Routes), Routes: []topology.Route{}, Listeners: []topology.Listener{}, UDPActive: g.udpActive.Load(), LeaseUntil: r.lease.Load()}
	list := []*Route{}
	keys := []string{}
	for k := range r.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		list = append(list, r.entries[k].route)
	}
	groups := r.endpointGroupsLocked(list, f.At)
	for i, v := range list {
		if i >= topology.MaxRoutes {
			f.Truncated = true
			break
		}
		state := "ready"
		if r.current[v.Config.ID] != r.entries[keys[i]] {
			state = "draining"
		} else if !v.Instance.Ready.Load() {
			state = "core_not_ready"
		} else if !r.entryReadyLocked(r.entries[keys[i]]) {
			state = "reserve_or_blocked"
		}
		source, _, _ := strings.Cut(v.Config.CapacityGroup, "/")
		if !safeTopologyID(source) {
			source = "unknown"
		}
		f.Routes = append(f.Routes, topology.Route{ProfileID: v.Config.Profile(), Egress: v.Config.EgressID, ID: v.Config.ID, Engine: keys[i], Core: v.Config.Core, Version: v.Instance.Config.Version, BuildSHA256: v.Instance.SHA256, Subscription: source, ServerHost: safeTopologyHost(endpointHost(v)), ServerIPs: append([]string{}, r.routeIPsLocked(v, f.At)...), DNSAt: r.endpointIPs[endpointHost(v)].Observed, Group: groups[i], State: state, Active: v.Active.Load(), Connections: v.Connections.Load(), Failures: v.Failures.Load(), Up: v.BytesUp.Load(), Down: v.BytesDown.Load(), SpeedBPS: v.SpeedBPS.Load(), MeasurementAt: v.LastSpeed.Load(), RTTUS: v.RTTUS.Load(), ValidUntil: r.valid[v.Config.ID]})
	}
	for i, l := range r.config.Listeners {
		if i >= 64 {
			f.Truncated = true
			break
		}
		f.Listeners = append(f.Listeners, topology.Listener{ID: l.ID, Protocol: l.Protocol, Policy: l.Policy})
	}
	g.Topology.Fill(f)
	return f
}
func safeTopologyID(s string) bool {
	if len(s) > 48 || len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func safeTopologyHost(s string) string {
	if ip, err := netip.ParseAddr(s); err == nil {
		return ip.Unmap().String()
	}
	if len(s) > 253 {
		return ""
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			return ""
		}
	}
	return s
}
