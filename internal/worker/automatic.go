package worker

import (
	"math"
	"proxyplatform/internal/topology"
	"time"
)

// automaticWeights is intentionally independent of router telemetry. Capacity
// is a fresh measured estimate or a delivered lower bound, never an idle rate.
// Shared endpoint aliases divide one capacity credit instead of inventing it.
func (r *Runtime) automaticWeights(p *policyState, routes []*Route, now time.Time) map[*Route]float64 {
	groups := r.endpointGroupsLocked(routes, now)
	counts := map[string]int{}
	for i, v := range routes {
		counts[groups[i]+"/"+v.Config.EgressID]++
	}
	raw := map[*Route]float64{}
	total := 0.0
	for i, v := range routes {
		capacity := int64(0)
		if now.Unix()-v.LastSpeed.Load() < 900 {
			capacity = v.SpeedBPS.Load()
		}
		if now.Unix()-v.LastPassive.Load() < 30 {
			capacity = max(capacity, v.PassiveBPS.Load())
		}
		freshTraffic := now.Unix()-v.TrafficAt.Load() < 5
		if freshTraffic {
			capacity = max(capacity, v.TrafficDownBPS.Load())
		}
		if v.CeilingBPS > 0 {
			capacity = min(capacity, v.CeilingBPS)
		}
		// One MiB/s is a conservative starting prior, not a capacity claim.
		credit := 1.0
		if capacity > 0 {
			credit = math.Max(.05, float64(capacity)/(1<<20))
		}
		latency := float64(v.RTTUS.Load()) / 1000
		if v.Instance != nil {
			latency = max(latency, float64(v.Instance.LatencyUS.Load())/1000)
		}
		if latency <= 0 {
			latency = 200
		}
		latencyScale := 100.0
		if p.config.Mode == "throughput" {
			latencyScale = 1000
		}
		errorCost := 1 + float64(v.ErrorEWMA.Load())/100
		cost := (1 + latency/latencyScale) * errorCost * errorCost * (1 + float64(v.ConsecutiveFailures.Load()))
		if freshTraffic {
			if measured := v.SpeedBPS.Load(); measured > 0 && now.Unix()-v.LastSpeed.Load() < 900 {
				utilization := float64(v.TrafficDownBPS.Load()) / float64(measured)
				cost *= 1 + 4*max(0, min(2, (utilization-.7)/.3))
			}
		}
		weight := credit / cost / float64(counts[groups[i]+"/"+v.Config.EgressID])
		if path, ok := r.config.Egress(v.Config.EgressID); ok && path.WeightMode == "manual" {
			weight *= float64(path.Weight)
		}
		raw[v] = weight
		total += weight
	}
	// The exploration allowance belongs to endpoint groups, not provider aliases.
	for i, v := range routes {
		group := groups[i] + "/" + v.Config.EgressID
		raw[v] = .95*raw[v]/max(total, 1e-9) + .05/float64(len(counts)*counts[group])
	}
	if p.autoWeights == nil {
		p.autoWeights = map[*Route]float64{}
	}
	if p.autoAt.IsZero() || now.Sub(p.autoAt) >= 5*time.Second {
		step := min(.1, .1*now.Sub(p.autoAt).Seconds()/30)
		for v, w := range raw {
			if old, ok := p.autoWeights[v]; ok && math.Abs(w-old) > .15*max(old, .01) {
				// Thirty-second smoothing, bounded growth. A failing route is
				// excluded by eligibility immediately, independent of this filter.
				w = old + max(-step, min(step, (w-old)/6))
			}
			p.autoWeights[v] = w
		}
		for v := range p.autoWeights {
			if _, ok := raw[v]; !ok {
				delete(p.autoWeights, v)
			}
		}
		p.autoAt = now
	}
	out := map[*Route]float64{}
	for v := range raw {
		if w, ok := p.autoWeights[v]; ok {
			out[v] = w
		} else {
			out[v] = raw[v]
		}
	}
	return out
}

func (r *Runtime) selectAutomaticLocked(p *policyState, routes []*Route, d *topology.Decision) []*Route {
	limit := min(len(routes), max(1, p.config.ActivePaths))
	weights := r.automaticWeights(p, routes[:limit], time.Now())
	if p.autoCredit == nil {
		p.autoCredit = map[*Route]float64{}
	}
	var chosen *Route
	total := 0.0
	for _, v := range routes[:limit] {
		w := weights[v]
		total += w
		p.autoCredit[v] += w
		if chosen == nil || p.autoCredit[v] > p.autoCredit[chosen] {
			chosen = v
		}
	}
	p.autoCredit[chosen] -= total
	for v := range p.autoCredit {
		if _, ok := weights[v]; !ok {
			delete(p.autoCredit, v)
		}
	}
	result := []*Route{chosen}
	for _, v := range routes {
		if v != chosen {
			result = append(result, v)
		}
	}
	if d != nil {
		d.Method = "automatic_observed_weight_choice"
		for _, v := range routes[:limit] {
			w := weights[v]
			score := 1 / max(w, 1e-9)
			e := candidateEvidence(v, &score, "automatic_observed_weight")
			e.EgressEffectiveWeight = w
			e.Formula = "automatic_observed_weight"
			d.Candidates = append(d.Candidates, e)
		}
	}
	return result
}
