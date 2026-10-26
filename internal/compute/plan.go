// Package compute contains bounded, credential-free routing computation.
// A computation result is a proposal, never permission or network health truth.
package compute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strconv"
)

const PPM = 1_000_000
const MaxGroups = 16

func ValidID(id string) bool { return opaque(id) }

var ErrInvalid = errors.New("invalid_compute_message")

type Group struct {
	ID          string `json:"id"`
	Lane        string `json:"lane"`
	Baseline    uint32 `json:"baseline_ppm"`
	RTTMS       uint32 `json:"rtt_ms"`
	ErrorPPM    uint32 `json:"error_ppm"`
	Samples     uint32 `json:"samples"`
	CapacityBPS int64  `json:"capacity_bps"`
	BusyBPS     int64  `json:"busy_bps"`
}
type Snapshot struct {
	Version      int     `json:"version"`
	Installation string  `json:"installation"`
	Executor     string  `json:"executor"`
	Policy       string  `json:"policy"`
	Boot         string  `json:"boot"`
	Sequence     int64   `json:"sequence"`
	Generation   int64   `json:"generation"`
	RouteSet     string  `json:"route_set"`
	Objective    string  `json:"objective"`
	DemandBPS    int64   `json:"demand_bps"`
	Groups       []Group `json:"groups"`
}

func (s Snapshot) Validate() error {
	if s.Version != 1 || !opaque(s.Installation) || !opaque(s.Executor) || !opaque(s.Policy) || !opaque(s.Boot) || s.Sequence <= 0 || s.Sequence > 1<<53-1 || s.Generation <= 0 || s.Generation > 1<<53-1 || !digest(s.RouteSet) || s.Objective != "objective-v1" || s.DemandBPS < 0 || s.DemandBPS > 1e12 || len(s.Groups) < 1 || len(s.Groups) > MaxGroups {
		return ErrInvalid
	}
	var total uint64
	ids := make([]string, 0, len(s.Groups))
	previous := ""
	for _, g := range s.Groups {
		if !opaque(g.ID) || !opaque(g.Lane) || g.ID <= previous || g.Baseline > PPM || g.RTTMS < 1 || g.RTTMS > 10000 || g.ErrorPPM > PPM || g.CapacityBPS < 0 || g.CapacityBPS > 1e12 || g.BusyBPS < 0 || g.BusyBPS > 1e12 {
			return ErrInvalid
		}
		total += uint64(g.Baseline)
		previous = g.ID
		ids = append(ids, g.ID)
	}
	if total != PPM {
		return ErrInvalid
	}
	set, err := Hash(ids)
	if err != nil || set != s.RouteSet {
		return ErrInvalid
	}
	return nil
}

func opaque(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func digest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == stringLower(s)
}
func stringLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'F' {
			b[i] = c + 32
		}
	}
	return string(b)
}

type Candidate struct {
	ID      uint32   `json:"id"`
	Weights []uint32 `json:"weights_ppm"`
}
type Metrics struct{ Cost, Latency, Error float64 }

// Generate uses the same immutable snapshot and finite candidate set on every
// machine. Whole candidates, not fragments, can be distributed independently.
func Generate(s Snapshot) ([]Candidate, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	base := make([]uint32, len(s.Groups))
	for i, g := range s.Groups {
		base[i] = g.Baseline
	}
	out := []Candidate{{ID: 0, Weights: base}}
	type move struct {
		from, to int
		delta    uint32
		hash     [32]byte
	}
	moves := make([]move, 0, len(base)*len(base)*3)
	seed, _ := Hash(s)
	for i, a := range s.Groups {
		for j, b := range s.Groups {
			if i == j || a.Samples < 20 || b.Samples < 20 {
				continue
			}
			if a.CapacityBPS == 0 || b.CapacityBPS == 0 {
				continue
			}
			for _, delta := range []uint32{25000, 50000, 100000} {
				if base[i] < delta {
					continue
				}
				moves = append(moves, move{i, j, delta, sha256.Sum256([]byte(seed + "/" + a.ID + "/" + b.ID + "/" + strconv.FormatUint(uint64(delta), 10)))})
			}
		}
	}
	sort.Slice(moves, func(i, j int) bool {
		return hex.EncodeToString(moves[i].hash[:]) < hex.EncodeToString(moves[j].hash[:])
	})
	for _, m := range moves[:min(63, len(moves))] {
		w := append([]uint32(nil), base...)
		w[m.from] -= m.delta
		w[m.to] += m.delta
		out = append(out, Candidate{uint32(len(out)), w})
	}
	return out, nil
}

// Evaluate recomputes a small surrogate locally. It never accepts a worker's
// claimed score, and cannot prove actual network capacity or user QoE.
func Evaluate(ctx context.Context, s Snapshot, c Candidate) (Metrics, error) {
	var result Metrics
	if err := s.Validate(); err != nil {
		return result, err
	}
	if len(c.Weights) != len(s.Groups) {
		return result, ErrInvalid
	}
	var total uint64
	for _, w := range c.Weights {
		if w > PPM {
			return result, ErrInvalid
		}
		total += uint64(w)
	}
	if total != PPM {
		return result, ErrInvalid
	}
	for i, g := range s.Groups {
		if (g.CapacityBPS == 0 || g.Samples < 20) && c.Weights[i] != g.Baseline {
			return result, ErrInvalid
		}
	}
	costs := make([]float64, 32)
	for scenario := range 32 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		q := (float64(scenario) + .5) / 32
		laneCap := map[string]float64{}
		latency, errRate, variation := 0.0, 0.0, 0.0
		for i, g := range s.Groups {
			p := float64(c.Weights[i]) / PPM
			p0 := float64(g.Baseline) / PPM
			laneCap[g.Lane] += p - p0
			rtt := float64(g.RTTMS) * (.75 + .5*q)
			penalty := rtt
			if g.CapacityBPS > 0 && s.DemandBPS > 0 {
				cap := float64(g.CapacityBPS) * (.70 + .30*q)
				rho := (float64(g.BusyBPS) + float64(s.DemandBPS)*p) / cap
				base := (float64(g.BusyBPS) + float64(s.DemandBPS)*p0) / cap
				if rho > .95 && rho > base+1e-12 {
					return result, ErrInvalid
				}
				penalty = rtt / math.Max(.05, 1-rho)
				// No ISP capacity observation is available here: do not infer it
				// from a sum of proxy capacities. Enforce unchanged ISP shares.
			}
			latency += p * penalty
			errRate += p * float64(g.ErrorPPM) / PPM
			variation += math.Abs(p - p0)
		}
		for lane, delta := range laneCap {
			if math.Abs(delta) > 1e-12 {
				return result, ErrInvalid
			}
			_ = lane
		}
		costs[scenario] = latency + 2000*errRate + 50*variation
		result.Latency = math.Max(result.Latency, latency)
		result.Error = math.Max(result.Error, errRate)
		result.Cost += costs[scenario] / 32
	}
	sort.Float64s(costs)
	result.Cost += .2 * costs[30]
	return result, nil
}

func Search(ctx context.Context, s Snapshot, first, count uint32) (Candidate, error) {
	all, err := Generate(s)
	if err != nil {
		return Candidate{}, err
	}
	if count == 0 || count > 64 || first > uint32(len(all)) {
		return Candidate{}, ErrInvalid
	}
	best := all[0]
	bm, err := Evaluate(ctx, s, best)
	if err != nil {
		return Candidate{}, err
	}
	for _, c := range all[first:min(uint32(len(all)), first+count)] {
		m, e := Evaluate(ctx, s, c)
		if e != nil {
			if ctx.Err() != nil {
				return Candidate{}, ctx.Err()
			}
			continue
		}
		if m.Cost < bm.Cost-1e-9 || (math.Abs(m.Cost-bm.Cost) <= 1e-9 && c.ID < best.ID) {
			best, bm = c, m
		}
	}
	return best, nil
}

func Verify(ctx context.Context, s Snapshot, c Candidate) error {
	all, err := Generate(s)
	if err != nil {
		return err
	}
	if int(c.ID) >= len(all) {
		return ErrInvalid
	}
	expected := all[c.ID]
	if len(expected.Weights) != len(c.Weights) {
		return ErrInvalid
	}
	for i, w := range expected.Weights {
		if c.Weights[i] != w {
			return ErrInvalid
		}
	}
	base, e := Evaluate(ctx, s, all[0])
	if e != nil {
		return e
	}
	m, e := Evaluate(ctx, s, c)
	if e != nil {
		return e
	}
	if c.ID == 0 || m.Cost > base.Cost*.95 || m.Latency > base.Latency*1.05 || m.Error > base.Error+.002 {
		return ErrInvalid
	}
	return nil
}
