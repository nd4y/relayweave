package worker

import "proxyplatform/internal/topology"

// Smooth weighted scheduling operates on ISP lanes, never profile counts.
// Aggregate live sessions reduce a lane's share while it carries long flows.
func (r *Runtime) selectEgressLocked(p *policyState, routes []*Route, d *topology.Decision) []*Route {
	if p.egressCredit == nil {
		p.egressCredit = map[string]float64{}
	}
	lanes := map[string][]*Route{}
	active := map[string]int64{}
	for _, v := range routes[:min(len(routes), p.config.ActivePaths)] {
		lanes[v.Config.EgressID] = append(lanes[v.Config.EgressID], v)
	}
	for _, e := range r.entries {
		active[e.route.Config.EgressID] += e.route.Active.Load()
	}
	chosen := ""
	total := float64(0)
	for _, path := range r.config.EgressPaths {
		if !path.Enabled || len(lanes[path.ID]) == 0 {
			delete(p.egressCredit, path.ID)
			continue
		}
		weight := float64(path.Weight) / (1 + float64(active[path.ID])/float64(path.Weight))
		total += weight
		p.egressCredit[path.ID] += weight
		if chosen == "" || p.egressCredit[path.ID] > p.egressCredit[chosen] {
			chosen = path.ID
		}
	}
	if chosen == "" {
		return nil
	}
	p.egressCredit[chosen] -= total
	lane := lanes[chosen]
	best := lane[0]
	var bestEvidence topology.Candidate
	var capture *topology.Candidate
	if d != nil {
		capture = &bestEvidence
	}
	score := best.scoreObserved(p.config.Mode, capture)
	for _, v := range lane[1:] {
		var evidence topology.Candidate
		capture = nil
		if d != nil {
			capture = &evidence
		}
		if s := v.scoreObserved(p.config.Mode, capture); s < score {
			best, score = v, s
			bestEvidence = evidence
		}
	}
	result := []*Route{best}
	for _, v := range lane {
		if v != best {
			result = append(result, v)
		}
	}
	for _, v := range routes {
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
		evidence.Score = &score
		evidence.Reason = "isp_weighted_path_selected"
		path, _ := r.config.Egress(chosen)
		evidence.EgressWeight = path.Weight
		evidence.EgressActive = active[chosen]
		evidence.EgressEffectiveWeight = float64(path.Weight) / (1 + float64(active[chosen])/float64(path.Weight))
		d.Candidates = append(d.Candidates, evidence)
	}
	return result
}
