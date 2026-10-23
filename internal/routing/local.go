// Package routing implements the bounded, network-independent local-v1 policy.
// It does not own permissions, probe budgets, engines, or socket lifetimes.
package routing

import (
	"math"
	"sort"
	"sync"
	"time"
)

const MaxGroups = 16
const Million = 1_000_000

// Monotonic time drives EWMA updates. Wall discontinuity/suspend may only
// discard stale confidence; it cannot make an older observation fresher.
func staleObservation(last, now time.Time) bool {
	if last.IsZero() || now.Before(last) || now.Sub(last) > 300*time.Second {
		return true
	}
	wall := now.Unix() - last.Unix()
	return wall < 0 || wall > 300
}

// Estimator is deliberately per-route and bounded; no flow history is retained.
type Estimator struct {
	mu           sync.Mutex
	last, window time.Time
	rtt, errors  float64
	samples      uint32
}

// RateEstimator smooths delivered rate observations; it never labels them as
// capacity. Missing observations do not create an estimate.
type RateEstimator struct {
	mu    sync.Mutex
	last  time.Time
	value float64
}

func (e *RateEstimator) Observe(now time.Time, bps float64) float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !finite(bps) || bps < 0 {
		return e.value
	}
	if staleObservation(e.last, now) {
		e.value = bps
	} else {
		a := -math.Expm1(-now.Sub(e.last).Seconds() / 10)
		e.value += a * (bps - e.value)
	}
	e.last = now
	return e.value
}

func (e *Estimator) Observe(now time.Time, rttMS float64, failed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	reset := staleObservation(e.last, now)
	if reset {
		e.rtt, e.errors, e.samples, e.window = 200, .02, 0, now
	}
	dt := now.Sub(e.last).Seconds()
	if reset {
		dt = 30
	}
	a := -math.Expm1(-math.Max(0, dt) / 30)
	if !failed && finite(rttMS) && rttMS > 0 {
		e.rtt += a * (math.Max(1, math.Min(10000, rttMS)) - e.rtt)
	}
	err := 0.0
	if failed {
		err = 1
	}
	e.errors += a * (err - e.errors)
	if staleObservation(e.window, now) {
		e.samples, e.window = 0, now
	}
	if e.samples < math.MaxUint32 {
		e.samples++
	}
	e.last = now
}

func (e *Estimator) Read(now time.Time) (rtt, errorRate float64, samples uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if staleObservation(e.last, now) {
		return 200, .02, 0
	}
	n := e.samples
	if staleObservation(e.window, now) {
		n = 0
	}
	return e.rtt, e.errors, n
}

type Input struct {
	ID, Group, Egress                string
	RTTMS, ErrorRate, BytesPerSecond float64
	Active                           int64
	Known                            bool
}

type Lane struct {
	ID          string
	Weight      float64
	Active      int64
	Utilization float64
	Fresh       bool
}

type Share struct {
	ID    string
	PPM   uint32
	Score float64
}
type Plan struct {
	Shares [MaxGroups]Share
	N      int
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func Score(v Input) float64 {
	rtt := v.RTTMS
	if !finite(rtt) || rtt <= 0 {
		rtt = 200
	}
	rtt = math.Max(1, math.Min(10000, rtt))
	err := v.ErrorRate
	if !finite(err) || err < 0 {
		err = .02
	}
	s := rtt * (1 + float64(max(0, v.Active))/4) * (1 + 4*math.Min(1, err))
	if finite(v.BytesPerSecond) && v.BytesPerSecond > 0 {
		s /= math.Sqrt(math.Max(v.BytesPerSecond/1250000, .1))
	}
	return s
}

// Build operates on a small pre-admitted window. Group aliases consume one
// share; a lane's weight never depends on its number of subscription profiles.
func Build(in []Input, lanes []Lane) Plan {
	var p Plan
	if len(in) > 256 {
		in = in[:256]
	}
	items := append([]Input(nil), in...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	groups := make(map[string]Input, len(items))
	groupActive := make(map[string]int64, len(items))
	groupRate := make(map[string]float64, len(items))
	groupKnown := make(map[string]bool, len(items))
	for _, v := range items {
		if v.ID == "" {
			continue
		}
		if v.Group == "" {
			v.Group = v.ID
		}
		key := v.Egress + "/" + v.Group
		// Aliases cannot reset the shared server's live load by presenting a
		// fresh route ID. Rates are lower bounds, so take max, never sum.
		groupActive[key] += max(0, v.Active)
		if finite(v.BytesPerSecond) {
			groupRate[key] = math.Max(groupRate[key], v.BytesPerSecond)
		}
		groupKnown[key] = groupKnown[key] || v.Known
		old, ok := groups[key]
		if !ok || Score(v) < Score(old) {
			groups[key] = v
		}
	}
	items = items[:0]
	for key, v := range groups {
		v.Active = groupActive[key]
		v.BytesPerSecond = groupRate[key]
		v.Known = groupKnown[key]
		items = append(items, v)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if len(items) > MaxGroups {
		items = items[:MaxGroups]
	}
	weights := make([]float64, len(items))
	known := map[string]bool{}
	totals := map[string]float64{}
	for _, v := range items {
		if v.Known {
			known[v.Egress] = true
		}
	}
	for i, v := range items {
		weights[i] = 1 / math.Max(Score(v), 1e-6)
		totals[v.Egress] += weights[i]
	}
	laneWeights := map[string]float64{}
	if len(lanes) == 0 {
		laneWeights[""] = 1
	} else {
		for _, l := range lanes {
			if !finite(l.Weight) || l.Weight <= 0 {
				continue
			}
			t := 1.0
			if l.Fresh && finite(l.Utilization) && l.Utilization >= 0 {
				t = math.Max(.5, math.Min(1, 1-2*math.Max(l.Utilization-.60, 0)))
			}
			laneWeights[l.ID] = l.Weight / (1 + float64(max(0, l.Active))/l.Weight) * t
		}
	}
	// Cap unknown groups at 1% of a lane only when it has known alternatives.
	// All-unknown cold start must still have a valid normalized distribution.
	for lane, total := range totals {
		unknownTotal, knownTotal := 0.0, 0.0
		for i, v := range items {
			if v.Egress != lane {
				continue
			}
			weights[i] /= total
			if !v.Known && known[lane] {
				weights[i] = math.Min(weights[i], .01)
				unknownTotal += weights[i]
			} else {
				knownTotal += weights[i]
			}
		}
		unknownScale := 1.0
		if unknownTotal > .01 {
			unknownScale = .01 / unknownTotal
			unknownTotal = .01
		}
		for i, v := range items {
			if v.Egress != lane {
				continue
			}
			if !v.Known && known[lane] {
				weights[i] *= unknownScale
			}
			if v.Known && knownTotal > 0 && known[lane] {
				weights[i] *= (1 - unknownTotal) / knownTotal
			}
			weights[i] *= laneWeights[lane]
		}
	}
	total := 0.0
	for _, w := range weights {
		total += w
	}
	if !finite(total) || total <= 0 {
		return p
	}
	remainders := make([]float64, len(items))
	sum := uint32(0)
	for i, v := range items {
		x := weights[i] / total * Million
		p.Shares[i] = Share{v.ID, uint32(x), Score(v)}
		remainders[i] = x - math.Floor(x)
		sum += uint32(x)
	}
	for sum < Million {
		best := 0
		for i := range items {
			if remainders[i] > remainders[best] {
				best = i
			}
		}
		p.Shares[best].PPM++
		remainders[best] = -1
		sum++
	}
	p.N = len(items)
	return p
}

// Pick filters the immutable cache against current permissions/health without
// renormalizing or allocating. The caller retains the final admission guard.
func (p *Plan) Pick(ticket uint64, allowed func(string) bool) (string, float64) {
	var total uint64
	for i := 0; i < p.N; i++ {
		s := p.Shares[i]
		if allowed(s.ID) {
			total += uint64(s.PPM)
		}
	}
	if total == 0 {
		return "", 0
	}
	x := ticket % total
	for i := 0; i < p.N; i++ {
		s := p.Shares[i]
		if !allowed(s.ID) {
			continue
		}
		if x < uint64(s.PPM) {
			return s.ID, float64(s.PPM) / float64(total)
		}
		x -= uint64(s.PPM)
	}
	return "", 0
}

// Probability reports the marginal probability after current eligibility.
func (p *Plan) Probability(id string, allowed func(string) bool) float64 {
	var total, value uint64
	for i := 0; i < p.N; i++ {
		s := p.Shares[i]
		if allowed(s.ID) {
			total += uint64(s.PPM)
			if s.ID == id {
				value = uint64(s.PPM)
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(value) / float64(total)
}

// Smooth bounds ordinary distribution changes; additions/removals are an
// emergency rebuild and intentionally do not retain excluded route shares.
func Smooth(old, next Plan, maxDelta uint32) Plan {
	if old.N != next.N || old.N == 0 {
		return next
	}
	for i := 0; i < old.N; i++ {
		if old.Shares[i].ID != next.Shares[i].ID {
			return next
		}
	}
	largest := uint32(0)
	for i := 0; i < old.N; i++ {
		a, b := old.Shares[i].PPM, next.Shares[i].PPM
		var d uint32
		if a > b {
			d = a - b
		} else {
			d = b - a
		}
		largest = max(largest, d)
	}
	if largest <= maxDelta {
		return next
	}
	var sum uint32
	for i := 0; i < old.N; i++ {
		delta := int64(next.Shares[i].PPM) - int64(old.Shares[i].PPM)
		next.Shares[i].PPM = uint32(int64(old.Shares[i].PPM) + delta*int64(maxDelta)/int64(largest))
		sum += next.Shares[i].PPM
	}
	// Integer rounding leaves at most N ppm; restore exact normalization.
	for i := 0; sum < Million; i = (i + 1) % next.N {
		next.Shares[i].PPM++
		sum++
	}
	for i := 0; sum > Million; i = (i + 1) % next.N {
		if next.Shares[i].PPM > 0 {
			next.Shares[i].PPM--
			sum--
		}
	}
	return next
}
