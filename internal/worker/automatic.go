package worker

import (
	"math"
	"proxyplatform/internal/config"
	"proxyplatform/internal/routertelemetry"
	"proxyplatform/internal/topology"
	"time"
)

type ispObservation struct {
	at          time.Time
	utilization float64
	factor      float64
}

type allocationEvidence struct {
	Policy      string    `json:"policy"`
	SelectedAt  time.Time `json:"selected_at"`
	Base        float64   `json:"base_weight"`
	Effective   float64   `json:"effective_weight"`
	Mode        string    `json:"mode"`
	Active      int64     `json:"active_connections"`
	Router      string    `json:"telemetry_state"`
	ObservedAt  time.Time `json:"observed_at,omitempty"`
	Utilization *float64  `json:"wan_utilization,omitempty"`
	Factor      float64   `json:"wan_factor"`
}

func (r *Runtime) telemetryDownLocked(isp string, now time.Time) bool {
	s, ok := r.routerTelemetry.Signal(isp, now)
	return ok && s.LinkKnown && s.LinkDown
}

func (r *Runtime) egressEnabledLocked(isp string) bool {
	if len(r.config.EgressPaths) == 0 {
		return true
	}
	p, ok := r.config.Egress(isp)
	return ok && p.Enabled
}

// Configured capacities are priors, not speed-test results. Actual profile
// quality and shared live load adapt them; no user-supplied weights in auto.
func (r *Runtime) automaticISPWeight(p *policyState, path config.EgressPath, lane []*Route, active int64, now time.Time) allocationEvidence {
	a := allocationEvidence{Policy: p.config.ID, SelectedAt: now, Mode: "automatic", Router: "autonomous", Factor: 1, Active: active}
	prior := float64(path.DownloadMbps) / 100
	if prior <= 0 || p.config.Mode == "round-robin" {
		a.Mode = "configured"
		prior = float64(max(1, path.Weight))
	}
	a.Base = prior
	quality := 1.0
	if a.Mode == "automatic" {
		quality = 0
		for _, v := range lane {
			var e topology.Candidate
			v.scoreObserved(p.config.Mode, &e)
			latencyScale := 200.0
			if p.config.Mode == "throughput" {
				latencyScale = 20000
			}
			q := 1 / (1 + math.Min(e.EffectiveRTTMS, 1000)/latencyScale) / (1 + float64(e.ConsecutiveFailures)) / (1 + float64(e.ErrorEWMA)/250)
			// Fresh delivered throughput is only positive evidence. A low sample
			// never claims an upper bound on capacity or punishes idle profiles.
			if e.SpeedUsedBPS > 0 && v.CeilingBPS > 0 {
				q *= 1 + math.Min(1, float64(e.SpeedUsedBPS)/float64(v.CeilingBPS))
			}
			quality = math.Max(quality, q)
		}
		quality = math.Max(.05, quality)
	}
	if signal, ok := r.routerTelemetry.Signal(path.ID, now); ok {
		a.Router = "fresh"
		a.ObservedAt = signal.ObservedAt
		if signal.LinkKnown && signal.LinkDown {
			a.Router = "link_down"
			a.Factor = 0
			return a
		}
		if a.Mode == "automatic" && (signal.DownloadBPS != nil || signal.UploadBPS != nil) {
			util := 0.0
			if signal.DownloadBPS != nil && path.DownloadMbps > 0 {
				util = *signal.DownloadBPS / (float64(path.DownloadMbps) * 125000)
			}
			if signal.UploadBPS != nil && path.UploadMbps > 0 {
				util = math.Max(util, *signal.UploadBPS/(float64(path.UploadMbps)*125000))
			}
			util = math.Min(1.5, math.Max(0, util))
			if r.ispObservations == nil {
				r.ispObservations = map[string]ispObservation{}
			}
			old, exists := r.ispObservations[path.ID]
			if !exists || now.Sub(old.at) > routertelemetry.ConsumerMaxAge {
				old = ispObservation{at: signal.ObservedAt, utilization: util, factor: math.Max(.1, 1-util)}
			} else if !signal.ObservedAt.Equal(old.at) {
				// Deadband, EWMA and per-sample slew limit prevent oscillation.
				if math.Abs(util-old.utilization) > .05 {
					old.utilization = .75*old.utilization + .25*util
				}
				target := math.Max(.1, 1-old.utilization)
				old.factor = math.Max(old.factor*.75, math.Min(old.factor*1.25, target))
				old.at = signal.ObservedAt
			}
			r.ispObservations[path.ID] = old
			a.Utilization = &old.utilization
			a.Factor = old.factor
		} else if a.Mode == "automatic" {
			delete(r.ispObservations, path.ID)
		}
	} else {
		delete(r.ispObservations, path.ID)
	}
	a.Effective = prior * quality * a.Factor / (1 + float64(active)/math.Max(1, prior))
	return a
}

// Alias count must not amplify a shared endpoint. Schedule between IP groups
// using their best observed service score and aggregate group load; choose the
// healthiest/least-loaded profile within the winning group. No DNS/network I/O.
func (r *Runtime) selectAutomaticProfileLocked(p *policyState, lane []*Route, d *topology.Decision) *Route {
	if len(lane) == 0 {
		return nil
	}
	all := append([]*Route(nil), lane...)
	seenRoutes := map[*Route]bool{}
	for _, v := range lane {
		seenRoutes[v] = true
	}
	for _, e := range r.entries {
		if !seenRoutes[e.route] {
			all = append(all, e.route)
			seenRoutes[e.route] = true
		}
	}
	roots := r.endpointGroupsLocked(all, time.Now())
	canonical := map[string]string{}
	for i, v := range all {
		if old := canonical[roots[i]]; old == "" || v.Config.ID < old {
			canonical[roots[i]] = v.Config.ID
		}
	}
	for i := range roots {
		roots[i] = canonical[roots[i]]
	}
	groups := roots[:len(lane)]
	weights := map[string]float64{}
	groupActive := map[string]int64{}
	best := map[string]*Route{}
	bestScore := map[string]float64{}
	evidences := map[string]topology.Candidate{}
	for i, v := range lane {
		var e topology.Candidate
		score := v.scoreObserved(p.config.Mode, &e)
		id := groups[i]
		groupActive[id] += e.Active
		// Keep sampled and unsampled paths in the same service-cost units. An
		// idle/passive lower bound is not an upper capacity estimate. The prior
		// is deliberately modest and explicitly labelled, never a measurement.
		prior := 12500000.0
		if v.CeilingBPS > 0 {
			prior = math.Min(prior, float64(v.CeilingBPS))
		}
		estimate := float64(e.SpeedUsedBPS)
		if estimate <= 0 || e.SpeedSource == "delivered_lower_bound" || e.SpeedSource == "active_or_passive_equal" {
			estimate = math.Max(prior, estimate)
			if estimate == prior {
				e.SpeedSource = "capacity_prior"
			}
			e.SpeedUsedBPS = int64(estimate)
		}
		latencyScale := 200.0
		if p.config.Mode == "throughput" {
			latencyScale = 20000
		}
		score = (1 + float64(e.Active)) * 12500000 / estimate * (1 + math.Min(e.EffectiveRTTMS, 1000)/latencyScale) * (1 + float64(e.ConsecutiveFailures)) * (1 + float64(e.ErrorEWMA)/250)
		e.Formula = "automatic_capacity_load"
		load := 1 + float64(e.Active)
		weights[id] = math.Max(weights[id], load/math.Max(score, 1e-9))
		if best[id] == nil || score < bestScore[id] {
			best[id] = v
			bestScore[id] = score
			evidences[id] = e
		}
	}
	for i := len(lane); i < len(all); i++ {
		groupActive[roots[i]] += all[i].Active.Load()
	}
	peak := 0.0
	for id, w := range weights {
		weights[id] = w / (1 + float64(groupActive[id]))
		peak = math.Max(peak, weights[id])
	}
	if p.profileCredit == nil {
		p.profileCredit = map[string]map[string]float64{}
	}
	key := lane[0].Config.EgressID
	credits := p.profileCredit[key]
	if credits == nil {
		credits = map[string]float64{}
		p.profileCredit[key] = credits
	}
	for id := range credits {
		if _, ok := weights[id]; !ok {
			delete(credits, id)
		}
	}
	chosen := ""
	total := 0.0
	normalized := map[string]float64{}
	seen := map[string]bool{}
	for _, id := range groups {
		if seen[id] {
			continue
		}
		seen[id] = true
		w := weights[id]
		w = math.Max(.02, w/peak)
		normalized[id] = w
		credits[id] = math.Max(-16, math.Min(16, credits[id])) + w
		total += w
		if chosen == "" || credits[id] > credits[chosen] {
			chosen = id
		}
	}
	credits[chosen] -= total
	v := best[chosen]
	if d != nil {
		if len(d.Candidates) > 16 {
			d.Truncated = true
			d.Candidates = d.Candidates[:16]
		}
		seen = map[string]bool{}
		for _, id := range groups {
			if seen[id] {
				continue
			}
			seen[id] = true
			if len(d.Candidates) >= 32 {
				d.Truncated = true
				break
			}
			e := evidences[id]
			score := bestScore[id]
			e.Score = &score
			e.Reason = "automatic_profile_weight_compared"
			if id == chosen {
				e.Reason = "automatic_profile_weight_selected"
			}
			e.ProfileWeight = normalized[id]
			e.GroupActive = groupActive[id]
			d.Candidates = append(d.Candidates, e)
		}
	}
	return v
}
