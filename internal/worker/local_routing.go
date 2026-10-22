package worker

import (
	"math"
	"math/rand/v2"
	"strconv"
	"time"

	"proxyplatform/internal/routing"
	"proxyplatform/internal/topology"
)

// r.mu is held. The cached vector contains only previously admitted routes;
// every selection intersects it with the current permission/health filter.
func (r *Runtime) selectLocalLocked(p *policyState, routes []*Route, d *topology.Decision, now time.Time, udp bool) []*Route {
	index := 0
	if udp {
		index = 1
	}
	allowed := func(id string) bool {
		for _, v := range routes {
			if v.Config.ID == id {
				return r.localEgressAllowed(v.Config.EgressID)
			}
		}
		return false
	}
	plan := &p.localPlans[index]
	assisted := !udp && r.computeEligible(p)
	if assisted && rand.Uint64()%100 == 0 {
		plan = &p.computePlan
	}
	chosen, probability := plan.Pick(rand.Uint64(), allowed)
	if assisted && chosen != "" {
		probability = .99*p.localPlans[index].Probability(chosen, allowed) + .01*p.computePlan.Probability(chosen, allowed)
	}
	if chosen == "" {
		// No cached eligible route: invalidate immediately and retain only
		// currently permitted egresses. No stale vector can enable a WAN.
		p.localAt[index] = time.Time{}
		out := routes[:0]
		for _, v := range routes {
			if r.localEgressAllowed(v.Config.EgressID) {
				out = append(out, v)
			}
		}
		return out
	}
	for i, v := range routes {
		if v.Config.ID == chosen {
			routes[0], routes[i] = routes[i], routes[0]
			break
		}
	}
	// Even retry candidates must obey the operator egress fence.
	out := routes[:0]
	for _, v := range routes {
		if r.localEgressAllowed(v.Config.EgressID) {
			out = append(out, v)
		}
	}
	if d != nil {
		d.Method = "local_v1_weighted"
		ev := candidateEvidence(out[0], nil, "local_v1_selected")
		ev.Formula = "local_v1"
		ev.Factors = append(ev.Factors, "probability="+strconv.FormatFloat(probability, 'g', -1, 64))
		d.Candidates = append(d.Candidates, ev)
	}
	return out
}

func (r *Runtime) localEgressAllowed(id string) bool {
	if len(r.config.EgressPaths) == 0 {
		return id == ""
	}
	for _, p := range r.config.EgressPaths {
		if p.ID == id {
			return p.Enabled && p.Weight > 0
		}
	}
	return false
}

func (r *Runtime) prepareLocalLocked(p *policyState, routes []*Route, now time.Time, udp bool) {
	index := 0
	if udp {
		index = 1
	}

	limit := min(len(routes), max(1, p.config.ActivePaths), routing.MaxGroups)
	window := routes[:limit]
	groups := r.endpointGroupsLocked(window, now)
	inputs := make([]routing.Input, 0, limit)
	for i, v := range window {
		if !r.localEgressAllowed(v.Config.EgressID) || (udp && !v.Config.UDP) {
			continue
		}
		rtt, errRate, samples := v.localStats.Read(now)
		// Health checks measure the upstream path; loopback SOCKS handshake
		// must never hide a larger network observation.
		if v.Instance != nil {
			rtt = math.Max(rtt, float64(v.Instance.LatencyUS.Load())/1000)
		}
		rate := float64(0)
		if !udp {
			if age := now.Unix() - v.LastSpeed.Load(); age >= 0 && age < 900 {
				rate = float64(v.SpeedBPS.Load())
			}
			if age := now.Unix() - v.LastPassive.Load(); age >= 0 && age < 30 {
				rate = math.Max(rate, float64(v.PassiveBPS.Load()))
			}
			if v.CeilingBPS > 0 {
				rate = math.Min(rate, float64(v.CeilingBPS))
			}
		}
		inputs = append(inputs, routing.Input{ID: v.Config.ID, Group: groups[i], Egress: v.Config.EgressID, RTTMS: rtt, ErrorRate: errRate, BytesPerSecond: rate, Active: v.Active.Load(), Known: samples >= 20 && rate > 0})
	}
	lanes := make([]routing.Lane, 0, len(r.config.EgressPaths))
	for _, path := range r.config.EgressPaths {
		if !path.Enabled {
			continue
		}
		var active int64
		for _, entry := range r.entries {
			if entry.route.Config.EgressID == path.ID {
				active += entry.route.Active.Load()
			}
		}
		lanes = append(lanes, routing.Lane{ID: path.ID, Weight: float64(path.Weight), Active: active})
	}
	plan := routing.Build(inputs, lanes)
	p.localPlans[index] = routing.Smooth(p.localPlans[index], plan, 100000)
	p.localAt[index] = now

}

// No estimator update, DNS, probe or plan construction runs on connection open.
func (r *Runtime) localBackground() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case now := <-tick.C:
			r.refreshComputePolicy()
			r.mu.Lock()
			for _, p := range r.policies {
				if p.config.Mode != "auto" {
					continue
				}
				active := int64(0)
				for _, v := range p.best {
					active += v.Active.Load()
				}
				if active == 0 && !p.localAt[0].IsZero() && now.Sub(p.localAt[0]) < 10*time.Second {
					continue
				}
				eligible := make([]*Route, 0, len(p.best))
				for _, v := range p.best {
					until := r.valid[v.Config.ID]
					if r.permitted(p.config.ID, v.Config) && v.Instance != nil && v.Instance.Ready.Load() && (until.IsZero() || now.Before(until)) && v.BlockedUntil.Load() <= now.UnixNano() {
						eligible = append(eligible, v)
					}
				}
				r.prepareLocalLocked(p, eligible, now, false)
				r.prepareLocalLocked(p, eligible, now, true)
			}
			r.mu.Unlock()
		}
	}
}
