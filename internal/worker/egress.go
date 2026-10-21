package worker

import (
	"math"
	"proxyplatform/internal/topology"
	"time"
)

// Smooth weighted scheduling operates on ISP lanes, never profile counts.
// Aggregate live sessions reduce a lane's share while it carries long flows.
func (r *Runtime) selectEgressLocked(p *policyState, routes []*Route, d *topology.Decision) []*Route {
	if p.egressCredit == nil {
		p.egressCredit = map[string]float64{}
	}
	lanes := map[string][]*Route{}
	active := map[string]int64{}
	for _, v := range routes[:min(len(routes), p.config.ActivePaths)] {
		if r.telemetryDownLocked(v.Config.EgressID, time.Now()) {
			continue
		}
		lanes[v.Config.EgressID] = append(lanes[v.Config.EgressID], v)
	}
	for _, e := range r.entries {
		active[e.route.Config.EgressID] += e.route.Active.Load()
	}
	chosen := ""
	total := float64(0)
	allocations := map[string]allocationEvidence{}
	for _, path := range r.config.EgressPaths {
		if !path.Enabled || len(lanes[path.ID]) == 0 {
			delete(p.egressCredit, path.ID)
			continue
		}
		a := r.automaticISPWeight(p, path, lanes[path.ID], active[path.ID], time.Now())
		weight := a.Effective
		if weight <= 0 {
			delete(p.egressCredit, path.ID)
			continue
		}
		allocations[path.ID] = a
		total += weight
		p.egressCredit[path.ID] = math.Max(-10000, math.Min(10000, p.egressCredit[path.ID])) + weight
		if chosen == "" || p.egressCredit[path.ID] > p.egressCredit[chosen] {
			chosen = path.ID
		}
	}
	r.ispAllocations = allocations
	if chosen == "" {
		return nil
	}
	p.egressCredit[chosen] -= total
	lane := lanes[chosen]
	best := lane[0]
	if p.config.Mode == "auto" || p.config.Mode == "throughput" {
		best = r.selectAutomaticProfileLocked(p, lane, d)
	} else {
		for _, v := range lane[1:] {
			if v.score(p.config.Mode) < best.score(p.config.Mode) {
				best = v
			}
		}
	}
	var bestEvidence topology.Candidate
	var capture *topology.Candidate
	if d != nil {
		capture = &bestEvidence
	}
	score := best.scoreObserved(p.config.Mode, capture)
	result := []*Route{best}
	for _, v := range lane {
		if v != best {
			result = append(result, v)
		}
	}
	for _, v := range routes {
		path, ok := r.config.Egress(v.Config.EgressID)
		if !ok || !path.Enabled || r.telemetryDownLocked(v.Config.EgressID, time.Now()) {
			continue
		}
		found := false
		for _, selected := range result {
			if selected == v {
				found = true
				break
			}
		}
		if !found {
			result = append(result, v)
		}
	}
	if d != nil {
		d.Method = "isp_weighted_path_choice"
		evidence := bestEvidence
		// Preserve operands captured by the actual automatic profile choice.
		for i := len(d.Candidates) - 1; i >= 0; i-- {
			if d.Candidates[i].Route == best.Config.ID && d.Candidates[i].Reason == "automatic_profile_weight_selected" {
				evidence = d.Candidates[i]
				d.Candidates = append(d.Candidates[:i], d.Candidates[i+1:]...)
				break
			}
		}
		if evidence.Score == nil {
			evidence.Score = &score
		}
		evidence.Reason = "isp_weighted_path_selected"
		path, _ := r.config.Egress(chosen)
		evidence.EgressWeight = path.Weight
		evidence.EgressActive = active[chosen]
		a := allocations[chosen]
		evidence.EgressEffectiveWeight = a.Effective
		evidence.EgressBaseWeight = a.Base
		evidence.WeightMode = a.Mode
		evidence.TelemetryState = a.Router
		if !a.ObservedAt.IsZero() {
			at := a.ObservedAt
			evidence.TelemetryAt = &at
		}
		evidence.WANUtilization = a.Utilization
		evidence.WANFactor = a.Factor
		d.Candidates = append(d.Candidates, evidence)
	}
	return result
}
