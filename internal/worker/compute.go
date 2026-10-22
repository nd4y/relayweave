package worker

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"math"
	"os"
	"sort"
	"time"

	"proxyplatform/internal/compute"
	"proxyplatform/internal/routing"
)

func (r *Runtime) routingAlgorithm() string {
	if r.localV1 {
		return "local-v1"
	}
	return "legacy"
}

// Caller holds r.mu. No network access or mutable remote state is consulted.
func (r *Runtime) computeState() string {
	if r.computeNoBenefit.Load() {
		return "local_no_measured_benefit"
	}
	if r.computeCache == nil {
		return "off"
	}
	if r.computeDisabled.Load() || r.computeEpoch <= 0 {
		return "degraded"
	}
	return r.computeMode
}

func readComputeKeys(path string) (map[string]ed25519.PublicKey, [32]byte, error) {
	var hash [32]byte
	raw, e := os.ReadFile(path)
	if e != nil || len(raw) > 4096 {
		return nil, hash, compute.ErrInvalid
	}
	hash = sha256.Sum256(raw)
	keys := map[string]ed25519.PublicKey{}
	if compute.StrictDecode(raw, &keys) != nil || len(keys) < 1 || len(keys) > 2 {
		return nil, hash, compute.ErrInvalid
	}
	for id, key := range keys {
		if !compute.ValidID(id) || len(key) != ed25519.PublicKeySize {
			return nil, hash, compute.ErrInvalid
		}
	}
	return keys, hash, nil
}

func (r *Runtime) configureCompute(executor string) error {
	path := os.Getenv("RELAYWEAVE_COMPUTE_KEYS")
	mode := os.Getenv("RELAYWEAVE_COMPUTE_MODE")
	if mode == "" {
		mode = "shadow"
	}
	if mode == "off" || path == "" {
		if mode == "canary" && path == "" {
			return compute.ErrInvalid
		}
		return nil
	}
	if !r.localV1 || (mode != "shadow" && mode != "canary") {
		return compute.ErrInvalid
	}
	keys, hash, e := readComputeKeys(path)
	if e != nil {
		return e
	}
	installation := os.Getenv("RELAYWEAVE_COMPUTE_INSTALLATION")
	if !compute.ValidID(installation) {
		return compute.ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.computeCache = compute.NewCache(keys)
	r.computeMode = mode
	r.computeInstallation = installation
	r.computeExecutor = executor
	r.computeKeyFile = path
	r.computeKeyHash = hash
	r.computeKeyRead = time.Now()
	return nil
}

func (r *Runtime) refreshComputePolicy() {
	r.mu.Lock()
	cache, path, lastRead, previous := r.computeCache, r.computeKeyFile, r.computeKeyRead, r.computeKeyHash
	r.mu.Unlock()
	if cache == nil {
		return
	}
	if _, e := os.Stat(path + ".disabled"); e == nil {
		r.computeDisabled.Store(true)
		cache.Clear()
		r.clearComputePlans()
		return
	} else if !os.IsNotExist(e) {
		r.computeDisabled.Store(true)
		cache.Clear()
		r.clearComputePlans()
		return
	}
	if time.Since(lastRead) < 5*time.Second {
		return
	}
	keys, hash, e := readComputeKeys(path)
	r.mu.Lock()
	r.computeKeyRead = time.Now()
	r.mu.Unlock()
	if e != nil {
		r.computeDisabled.Store(true)
		cache.Clear()
		r.clearComputePlans()
		return
	}
	if hash != previous {
		cache.SetKeys(keys)
		r.clearComputePlans()
		r.mu.Lock()
		r.computeKeyHash = hash
		r.mu.Unlock()
	}
	r.computeDisabled.Store(false)
}

// ComputeSnapshots exports only HMAC identifiers and numerical aggregates,
// at most two policies per bounded batch. Raw route configurations never leave.
func (r *Runtime) ComputeSnapshots() []compute.Snapshot {
	r.mu.Lock()
	if r.computeCache == nil || r.computeDisabled.Load() || r.computeNoBenefit.Load() || r.computeEpoch <= 0 || time.Since(r.computeLastExport) < 15*time.Second {
		r.mu.Unlock()
		return nil
	}
	r.computeLastExport = time.Now()
	now := time.Now()
	type pendingSnapshot struct {
		p      *policyState
		s      compute.Snapshot
		routes map[string]string
	}
	pending := []pendingSnapshot{}
	cache := r.computeCache
	ids := make([]string, 0, len(r.policies))
	for id := range r.policies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		offset := r.computePolicyCursor % len(ids)
		ids = append(ids[offset:], ids[:offset]...)
		r.computePolicyCursor = (offset + 2) % len(ids)
	}
	for _, id := range ids {
		if len(pending) >= 2 {
			break
		}
		p := r.policies[id]
		if p.config.Mode != "auto" || p.localPlans[0].N < 2 {
			continue
		}
		s := compute.Snapshot{Version: 1, Installation: r.computeInstallation, Executor: r.computeExecutor, Policy: r.computeCache.Opaque("policy/" + id), Generation: r.config.Generation, Objective: "objective-v1"}
		routes := map[string]string{}
		known := 0
		valid := true
		for i := 0; i < p.localPlans[0].N; i++ {
			share := p.localPlans[0].Shares[i]
			entry := r.current[share.ID]
			if entry == nil || !r.permitted(id, entry.route.Config) || !entry.route.Instance.Ready.Load() || !r.localEgressAllowed(entry.route.Config.EgressID) {
				valid = false
				break
			}
			v := entry.route
			rtt, errorRate, samples := v.localStats.Read(now)
			if v.Instance != nil {
				rtt = math.Max(rtt, float64(v.Instance.LatencyUS.Load())/1000)
			}
			capacity := int64(0)
			if age := now.Unix() - v.LastSpeed.Load(); age >= 0 && age < 900 {
				capacity = max(int64(0), v.SpeedBPS.Load())
				if v.CeilingBPS > 0 {
					capacity = min(capacity, v.CeilingBPS)
				}
			}
			if capacity > 0 && samples >= 20 {
				known++
			}
			group := r.computeCache.Opaque("route/" + share.ID)
			routes[group] = share.ID
			s.Groups = append(s.Groups, compute.Group{ID: group, Lane: r.computeCache.Opaque("lane/" + v.Config.EgressID), Baseline: share.PPM, RTTMS: uint32(math.Max(1, math.Min(10000, rtt))), ErrorPPM: uint32(math.Max(0, math.Min(1, errorRate)) * compute.PPM), Samples: samples, CapacityBPS: capacity})
		}
		if !valid || known < 2 {
			continue
		}
		sort.Slice(s.Groups, func(i, j int) bool { return s.Groups[i].ID < s.Groups[j].ID })
		pending = append(pending, pendingSnapshot{p, s, routes})
	}
	r.mu.Unlock()
	out := []compute.Snapshot{}
	for _, item := range pending {
		s := item.s
		set := make([]string, len(s.Groups))
		for i, g := range s.Groups {
			set[i] = g.ID
		}
		s.RouteSet, _ = compute.Hash(set)
		captured, e := cache.Capture(s)
		if e != nil {
			continue
		}
		// Calibrate before retaining online offload: identical finite searches
		// that fit locally do not justify continuous remote CPU/network use.
		call, done := context.WithTimeout(r.ctx, 10*time.Millisecond)
		_, localErr := compute.Search(call, captured, 0, 64)
		done()
		r.mu.Lock()
		if localErr == nil {
			r.computeCheapEpochs++
			if r.computeCheapEpochs >= 20 {
				r.computeNoBenefit.Store(true)
				for _, p := range r.policies {
					p.computePlan = routing.Plan{}
				}
			}
		} else {
			r.computeCheapEpochs = 0
		}
		if r.policies[item.p.config.ID] == item.p && r.config.Generation == s.Generation && !r.computeDisabled.Load() {
			item.p.computeSnapshot = captured
			item.p.computeRoutes = item.routes
			item.p.computePlan = routing.Plan{}
			out = append(out, captured)
		}
		r.mu.Unlock()
	}
	return out
}

func (r *Runtime) AcceptCompute(epoch int64, recommendations []compute.Recommendation) {
	r.mu.Lock()
	cache := r.computeCache
	if cache == nil {
		r.mu.Unlock()
		return
	}
	if epoch <= 0 {
		r.computeEpoch = 0
		for _, p := range r.policies {
			p.computePlan = routing.Plan{}
		}
		r.mu.Unlock()
		cache.Clear()
		return
	}
	if epoch < r.computeEpoch {
		r.mu.Unlock()
		return
	}
	r.computeEpoch = epoch
	r.mu.Unlock()
	cache.SetEpoch(epoch)
	if r.computeDisabled.Load() || len(recommendations) > 32 {
		return
	}
	for _, rec := range recommendations {
		r.mu.RLock()
		var p *policyState
		for _, candidate := range r.policies {
			if candidate.computeSnapshot.Policy == rec.Snapshot.Policy {
				p = candidate
				break
			}
		}
		generation := r.config.Generation
		routeSet := ""
		id := ""
		duplicate := false
		if p != nil {
			routeSet = p.computeSnapshot.RouteSet
			id = p.config.ID
			duplicate = p.computeEpoch == epoch && p.computeRecSequence == rec.Sequence
		}
		r.mu.RUnlock()
		if p == nil || duplicate {
			continue
		}
		call, cancel := context.WithTimeout(r.ctx, 10*time.Millisecond)
		weights, e := cache.Accept(call, rec, generation, routeSet)
		cancel()
		if e != nil {
			r.computeRejected.Add(1)
			continue
		}
		deadline, ok := cache.Deadline(rec.Snapshot.Policy, rec.Snapshot.Sequence)
		r.mu.Lock()
		if !ok || r.computeEpoch != epoch || r.config.Generation != generation || r.policies[id] != p || p.computeSnapshot.Sequence != rec.Snapshot.Sequence || r.computeDisabled.Load() {
			r.mu.Unlock()
			continue
		}
		plan := routing.Plan{N: len(weights)}
		valid := true
		for i, g := range p.computeSnapshot.Groups {
			routeID := p.computeRoutes[g.ID]
			entry := r.current[routeID]
			if entry == nil || !r.permitted(id, entry.route.Config) || !r.localEgressAllowed(entry.route.Config.EgressID) {
				valid = false
				break
			}
			plan.Shares[i] = routing.Share{ID: routeID, PPM: weights[i]}
		}
		if valid {
			p.computePlan = plan
			p.computeEpoch = epoch
			p.computeRecSequence = rec.Sequence
			p.computeDeadline = deadline
			r.computeAccepted.Add(1)
		}
		r.mu.Unlock()
	}
}

func computePlanFresh(deadline time.Duration) bool {
	now, e := compute.Elapsed()
	return e == nil && deadline > 0 && now <= deadline
}

func (r *Runtime) computeEligible(p *policyState) bool {
	return r.computeCache != nil && !r.computeDisabled.Load() && !r.computeNoBenefit.Load() && r.computeMode == "canary" && p.computeEpoch == r.computeEpoch && p.computePlan.N > 0 && computePlanFresh(p.computeDeadline)
}

func (r *Runtime) clearComputePlans() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.policies {
		p.computePlan = routing.Plan{}
		p.computeDeadline = 0
	}
}
