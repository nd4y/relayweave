package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/topology"
)

const hotLimit = 16
const processLimit = 32 // Includes retiring sessions and rollout candidates.
type engineEntry struct {
	staged  bool
	dialIP  string
	route   *Route
	manager *core.Manager
	cancel  context.CancelFunc
	key     string
	started time.Time
	used    time.Time
	slot    int
}
type probePermit struct {
	generation int64
	until      time.Time
}
type policyState struct {
	autoAt           time.Time
	autoWeights      map[*Route]float64
	autoCredit       map[*Route]float64
	topologyFactors  map[string]string
	topologyPrepared time.Time
	rank             map[string]int
	config           config.Policy
	best             []*Route
	chosen           string
	changed          time.Time
	egressCredit     map[string]float64
}
type Runtime struct {
	restoreRollovers []rolloverRecord
	rolloverDirty    atomic.Bool
	rollovers        map[string]*pathRollover
	ctx              context.Context
	mu               sync.RWMutex
	applyMu          sync.Mutex
	config           config.Config
	entries          map[string]*engineEntry
	current          map[string]*engineEntry
	policies         map[string]*policyState
	valid            map[string]time.Time
	slots            map[int]bool
	cursor           atomic.Uint64
	drained          atomic.Bool
	lease            atomic.Int64
	probeBytes       atomic.Int64
	probeWindow      time.Time
	budget           map[string]int64
	visited          map[string]time.Time
	coldCursor       int
	constraints      *config.Config
	liveGuard        *config.LiveGuard
	allowed          map[string]map[string]string
	hardExpiry       map[string]time.Time
	staging          map[string]probePermit
	admission        map[string]*atomic.Int64
	rootDir          string
	retiredUp        atomic.Int64
	retiredDown      atomic.Int64
	dnsMu            sync.Mutex
	endpointIPs      map[string]endpointResolution
	targetHealth     map[string]targetObservation
}

func NewRuntime(ctx context.Context, roots ...string) *Runtime {
	r := &Runtime{ctx: ctx, entries: map[string]*engineEntry{}, current: map[string]*engineEntry{}, policies: map[string]*policyState{}, valid: map[string]time.Time{}, slots: map[int]bool{}, budget: map[string]int64{}, visited: map[string]time.Time{}, probeWindow: time.Now()}
	r.rootDir = "/data/runtime"
	if len(roots) > 0 {
		r.rootDir = roots[0]
	}
	go r.background()
	return r
}
func (r *Runtime) Configuration() config.Config { r.mu.RLock(); defer r.mu.RUnlock(); return r.config }
func (r *Runtime) Listener(id string) (config.Listener, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c := r.config
	if r.constraints != nil {
		c = *r.constraints
	}
	for _, l := range c.Listeners {
		if l.ID == id {
			return l, true
		}
	}
	return config.Listener{}, false
}
func fingerprint(v config.Route) string {
	if v.CheckServer {
		copy := v
		copy.CheckServer = false
		sum := sha256.Sum256([]byte(fingerprint(copy) + "/" + config.ServerPolicyDigest(true, v.AllowPrivate, v.ServerAllowCIDRs)))
		return hex.EncodeToString(sum[:])
	}
	if v.EgressID != "" {
		sum := sha256.Sum256(append([]byte(v.Fingerprint+"/"+v.EgressID+"/"), v.Outbound...))
		return hex.EncodeToString(sum[:])
	}
	if v.Fingerprint != "" {
		return v.Fingerprint
	}
	sum := sha256.Sum256(v.Outbound)
	return hex.EncodeToString(sum[:])
}

// Revocations are applied before canary preparation. A failed rollout must never
// restore a removed profile, old credentials, an old password or a wider ACL.
func (r *Runtime) Restrict(c config.Config) {
	allowed := map[string]map[string]string{}
	routes := map[string]string{}
	expiry := map[string]time.Time{}
	for _, v := range c.Routes {
		routes[v.ID] = fingerprint(v)
		expiry[v.ID] = v.ValidUntil
	}
	for _, p := range c.Policies {
		m := map[string]string{}
		for _, id := range p.Routes {
			m[id] = routes[id]
		}
		allowed[p.ID] = m
	}
	r.mu.Lock()
	r.constraints = &c
	r.allowed = allowed
	r.hardExpiry = expiry
	r.mu.Unlock()
}
func (r *Runtime) permitted(policy string, v config.Route) bool {
	if r.allowed == nil && !v.ValidUntil.IsZero() && !time.Now().Before(v.ValidUntil) {
		return false
	}
	if r.liveGuard != nil {
		permit, ok := r.liveGuard.Routes[v.Profile()]
		expected := baseFingerprint(v)
		if permit.ServerPolicyDigest != "" && permit.ServerPolicyDigest != config.ServerPolicyDigest(v.CheckServer, v.AllowPrivate, v.ServerAllowCIDRs) {
			return false
		}
		if !ok || !permit.Permits(expected, time.Now(), true) {
			return false
		}
	}
	if until := r.hardExpiry[v.ID]; !until.IsZero() && !time.Now().Before(until) {
		return false
	}
	if r.allowed == nil {
		return true
	}
	if policy != "" {
		return r.allowed[policy][v.ID] == fingerprint(v) || r.overlapAllowedLocked(policy, v)
	}
	for _, m := range r.allowed {
		if m[v.ID] == fingerprint(v) {
			return true
		}
	}
	if r.overlapAllowedLocked("", v) {
		return true
	}
	return false
}
func (r *Runtime) RenewLease(t time.Time) { r.lease.Store(t.Unix()) }
func routeIdentity(c config.Route, version string) string {
	identity := c.Core + "/" + version + "/"
	if c.CheckServer {
		identity += config.ServerPolicyDigest(true, c.AllowPrivate, c.ServerAllowCIDRs) + "/"
	}
	sum := sha256.Sum256(append([]byte(identity), c.Outbound...))
	return c.ID + "-" + hex.EncodeToString(sum[:8])
}
func (r *Runtime) ensure(ctx context.Context, c config.Config, route config.Route) (*engineEntry, error) {
	var binary config.Core
	for _, v := range c.Cores {
		if v.ID == route.Core {
			binary = v
			break
		}
	}
	if c.Managed && len(binary.SHA256) != 64 {
		return nil, fmt.Errorf("artifact_binary_digest_missing")
	}
	checked, dialIP, err := checkedServer(ctx, route)
	if err != nil {
		return nil, err
	}
	key := routeIdentity(route, binary.Version+"/"+binary.SHA256)
	if dialIP != "" {
		sum := sha256.Sum256([]byte(dialIP))
		key += "-" + hex.EncodeToString(sum[:4])
	}
	r.mu.Lock()
	if r.staging == nil {
		r.staging = map[string]probePermit{}
	}
	r.staging[key] = probePermit{c.Generation, route.ValidUntil}
	if e := r.entries[key]; e != nil {
		r.mu.Unlock()
		return e, nil
	}
	if len(r.entries) >= processLimit {
		delete(r.staging, key)
		r.mu.Unlock()
		return nil, fmt.Errorf("engine_budget_exhausted")
	}
	staged := 0
	for _, e := range r.entries {
		if e.staged {
			staged++
		}
	}
	for _, plan := range r.rollovers {
		if plan.state == "warming" || plan.state == "canary" {
			staged++
		}
	}
	if staged >= 2 {
		delete(r.staging, key)
		r.mu.Unlock()
		return nil, fmt.Errorf("staging_budget_exhausted")
	}
	slot := 0
	for i := 0; i < processLimit; i++ {
		if !r.slots[i] {
			slot = i
			break
		}
	}
	r.slots[slot] = true
	r.mu.Unlock()
	if err := engineHeadroom(); err != nil {
		r.mu.Lock()
		delete(r.slots, slot)
		delete(r.staging, key)
		r.mu.Unlock()
		return nil, err
	}
	route.Endpoint = net.JoinHostPort("127.0.0.1", fmt.Sprint(41000+slot))
	cfg := c
	cfg.Cores = []config.Core{binary}
	cfg.Routes = []config.Route{route}
	checked.Endpoint = route.Endpoint
	cfg.Routes[0] = checked
	cfg.StateDir = filepath.Join(r.rootDir, key)
	cfg.ProbeIntervalSeconds = 30
	probeSettings := routeProbeConfig(c, route.ID)
	cfg.ProbeURL, cfg.ProbeFallbackURLs, cfg.ProbeIntervalSeconds = probeSettings.ProbeURL, probeSettings.ProbeFallbackURLs, probeSettings.ProbeIntervalSeconds
	cfg.ConnectTimeoutMS = probeSettings.ConnectTimeoutMS
	manager, err := core.New(cfg)
	if err != nil {
		r.mu.Lock()
		delete(r.slots, slot)
		delete(r.staging, key)
		r.mu.Unlock()
		return nil, fmt.Errorf("engine_validation_failed")
	}
	manager.Instances[binary.ID].ProbeAllowed = func() bool { return r.probeAllowed(key, route) }
	child, cancel := context.WithCancel(r.ctx)
	if err = manager.Start(child, cfg); err != nil {
		cancel()
		r.mu.Lock()
		delete(r.slots, slot)
		delete(r.staging, key)
		r.mu.Unlock()
		return nil, fmt.Errorf("engine_configuration_rejected")
	}
	e := &engineEntry{staged: true, dialIP: dialIP, route: &Route{Config: route, Instance: manager.Instances[binary.ID]}, manager: manager, cancel: cancel, key: key, started: time.Now(), used: time.Now(), slot: slot}
	e.route.CeilingBPS = int64(c.DownloadMbps) * 125000
	if path, ok := c.Egress(route.EgressID); ok {
		e.route.CeilingBPS = int64(path.DownloadMbps) * 125000
	}
	r.mu.Lock()
	r.entries[key] = e
	r.mu.Unlock()
	return e, nil
}

func (r *Runtime) probeAllowed(key string, route config.Route) bool {
	if time.Now().Unix() > r.lease.Load() {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.permitted("", route) {
		return false
	}
	until := route.ValidUntil
	pending, staged := r.staging[key]
	if staged && pending.generation > r.config.Generation {
		// A refreshed subscription must be able to revalidate an existing
		// process whose accepted TTL expired. This permit affects probes only;
		// Candidates still uses the accepted expiry until Apply succeeds.
		until = pending.until
	} else if active := r.current[route.ID]; active != nil && active.key == key {
		until = r.valid[route.ID]
	} else if plan := r.rollovers[route.ID]; plan != nil && plan.old.Config.Fingerprint == route.Fingerprint && r.overlapAllowedLocked("", route) {
		until = r.overlapDeadlineLocked(route)
	} else {
		return false
	}
	return until.IsZero() || time.Now().Before(until)
}
func (r *Runtime) Apply(ctx context.Context, c config.Config, g *Gateway) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if err := c.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	routes := map[string]config.Route{}
	for _, v := range c.Routes {
		routes[v.ID] = v
	}
	prepared := map[string]*engineEntry{}
	validated := map[string]bool{}
	validatedTargets := map[string]targetObservation{}
	// Staging is bounded. Every policy must have a healthy path before a revision is accepted.
	for _, p := range c.Policies {
		if len(p.Routes) == 0 {
			continue
		}
		healthy := false
		admissible := false
		resourceDeferred, rotation := false, false
		for _, id := range p.Routes {
			if err := ctx.Err(); err != nil {
				return err
			}
			v := routes[id]
			rotation = rotation || len(v.Previous) > 0
			if v.EstablishedOnly {
				continue
			}
			admissible = true
			if !v.ValidUntil.IsZero() && time.Now().After(v.ValidUntil) {
				continue
			}
			e, err := r.ensure(ctx, c, v)
			if err != nil {
				resourceDeferred = resourceDeferred || strings.HasPrefix(err.Error(), "staging_") || strings.HasPrefix(err.Error(), "engine_")
				continue
			}
			prepared[id] = e
			check := config.Config{ProbeURL: p.ProbeURL, ProbeFallbackURLs: p.ProbeFallbackURLs, ConnectTimeoutMS: p.ConnectTimeoutMS}
			if check.ProbeURL == "" {
				check.ProbeURL, check.ProbeFallbackURLs = c.ProbeURL, c.ProbeFallbackURLs
			}
			if check.ConnectTimeoutMS == 0 {
				check.ConnectTimeoutMS = c.ConnectTimeoutMS
			}
			healthy = e.route.Instance.CheckRoute(ctx, e.route.Config.Endpoint, check)
			if healthy {
				validated[id] = true
				validatedTargets[p.ID+"/"+id] = targetObservation{Signature: targetSignature(p), Checked: time.Now(), Healthy: true}
				break
			}
			delete(prepared, id)
			r.discardStaged(e)
		}
		if !healthy && admissible {
			// A bounded staging budget must not make independent policies
			// lose their authorized LKG path. Replace them lazily in maintain.
			for _, id := range p.Routes {
				r.mu.RLock()
				old := r.current[id]
				allowed := old != nil && r.permitted(p.ID, old.route.Config)
				r.mu.RUnlock()
				if !allowed || !old.route.Instance.Ready.Load() {
					continue
				}
				check := config.Config{ProbeURL: p.ProbeURL, ProbeFallbackURLs: p.ProbeFallbackURLs, ConnectTimeoutMS: p.ConnectTimeoutMS}
				if check.ProbeURL == "" {
					check.ProbeURL = c.ProbeURL
					check.ProbeFallbackURLs = c.ProbeFallbackURLs
				}
				if check.ConnectTimeoutMS == 0 {
					check.ConnectTimeoutMS = c.ConnectTimeoutMS
				}
				if !old.route.Instance.CheckRoute(ctx, old.route.Config.Endpoint, check) {
					continue
				}
				prepared[id] = old
				healthy = true
				break
			}
		}
		if !healthy && admissible {
			if rotation && resourceDeferred && len(prepared) > 0 {
				continue
			}
			return fmt.Errorf("policy_has_no_healthy_path")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := g.Reconfigure(c); err != nil {
		return err
	}
	recovered := r.restoreRolloverEngines(ctx, c)
	valid := map[string]time.Time{}
	policies := map[string]*policyState{}
	versions := map[string]string{}
	for _, b := range c.Cores {
		versions[b.ID] = b.Version + "/" + b.SHA256
	}
	r.mu.RLock()
	existing := make(map[string]*engineEntry, len(r.entries))
	existingCurrent := make(map[string]*engineEntry, len(r.current))
	for id, e := range r.current {
		existingCurrent[id] = e
	}
	for k, e := range r.entries {
		existing[k] = e
	}
	r.mu.RUnlock()
	for _, v := range c.Routes {
		valid[v.ID] = v.ValidUntil
		if prepared[v.ID] != nil {
			continue
		}
		base := routeIdentity(v, versions[v.Core])
		if e := existingCurrent[v.ID]; e != nil && (e.key == base || strings.HasPrefix(e.key, base+"-")) {
			prepared[v.ID] = e
			continue
		}
		if e := existingCurrent[v.ID]; e != nil {
			r.mu.RLock()
			overlap := r.overlapAllowedLocked("", e.route.Config)
			r.mu.RUnlock()
			if overlap {
				prepared[v.ID] = e
				continue
			}
		}
		for key, e := range existing {
			if key == base || (v.CheckServer && strings.HasPrefix(key, base+"-")) {
				prepared[v.ID] = e
				break
			}
		}
	}
	for _, p := range c.Policies {
		policies[p.ID] = newPolicyState(p)
	}
	probes := map[string]config.Config{}
	for id := range prepared {
		probes[id] = routeProbeConfig(c, id)
	}
	r.mu.Lock()
	r.config = c
	previousCurrent := r.current
	r.current = prepared
	r.installRolloversLocked(previousCurrent, time.Now())
	r.installRecoveredRolloversLocked(recovered)
	for id, e := range prepared {
		e.staged = false
		if validated[id] {
			e.route.Instance.ConfigureValidatedProbe(probes[id])
		} else {
			e.route.Instance.ConfigureProbe(probes[id])
		}
	}
	r.valid = valid
	for id, p := range policies {
		if old := r.policies[id]; old != nil && old.config.Mode == p.config.Mode {
			p.chosen, p.changed = old.chosen, old.changed
		}
	}
	r.policies = policies
	r.installTargetObservationsLocked(validatedTargets)
	if r.admission == nil {
		r.admission = map[string]*atomic.Int64{}
	}
	for id := range r.policies {
		if r.admission[id] == nil {
			r.admission[id] = &atomic.Int64{}
		}
	}
	for id, n := range r.admission {
		if r.policies[id] == nil && n.Load() == 0 {
			delete(r.admission, id)
		}
	}
	r.lease.Store(c.LeaseUntil.Unix())
	r.rebuildLocked()
	r.mu.Unlock()
	g.closeIdle()
	return nil
}

// A failed canary must release its staging slot so later catalogue candidates
// can be examined without exceeding the process budget. Existing LKG processes
// and sessions are retained.
func (r *Runtime) discardStaged(e *engineEntry) {
	r.mu.Lock()
	if r.current[e.route.Config.ID] == e || e.route.Active.Load() > 0 || e.route.References.Load() > 0 {
		r.mu.Unlock()
		return
	}
	e.cancel()
	r.mu.Unlock()
	e.manager.Wait()
	r.mu.Lock()
	if r.entries[e.key] == e {
		delete(r.entries, e.key)
		delete(r.staging, e.key)
		delete(r.slots, e.slot)
	}
	r.mu.Unlock()
	os.RemoveAll(filepath.Join(r.rootDir, e.key))
}
func routeProbeConfig(c config.Config, id string) config.Config {
	out := config.Config{ProbeURL: c.ProbeURL, ProbeFallbackURLs: c.ProbeFallbackURLs, ProbeIntervalSeconds: 30, ConnectTimeoutMS: c.ConnectTimeoutMS}
	targets := []string{}
	seen := map[string]bool{}
	policyTimeout := 0
	for _, p := range c.Policies {
		for _, profile := range p.Routes {
			if profile != id {
				continue
			}
			for _, url := range append([]string{p.ProbeURL}, p.ProbeFallbackURLs...) {
				if url != "" && !seen[url] && len(targets) < 16 {
					targets = append(targets, url)
					seen[url] = true
				}
			}
			if p.ProbeIntervalSeconds > 0 {
				out.ProbeIntervalSeconds = min(out.ProbeIntervalSeconds, p.ProbeIntervalSeconds)
			}
			budget := p.ConnectTimeoutMS
			if budget <= 0 {
				budget = c.ConnectTimeoutMS
			}
			policyTimeout = max(policyTimeout, budget)
			break
		}
	}
	if len(targets) > 0 {
		out.ProbeURL, out.ProbeFallbackURLs = targets[0], targets[1:]
	}
	// A shared core remains reachable for its most patient referencing policy.
	// Unrelated policies do not widen this budget, and a sole short policy
	// keeps its explicit timeout rather than inheriting the global default.
	if policyTimeout > 0 {
		out.ConnectTimeoutMS = policyTimeout
	}
	return out
}

func newPolicyState(p config.Policy) *policyState {
	ranks := make(map[string]int, len(p.Routes))
	for n, id := range p.Routes {
		ranks[id] = n
	}
	return &policyState{config: p, rank: ranks, changed: time.Now()}
}
func (r *Runtime) rebuildLocked() {
	now := time.Now()
	r.advanceRolloversLocked(now)
	for _, p := range r.policies {
		candidates := []*Route{}
		p.topologyFactors = map[string]string{}
		p.topologyPrepared = now
		for id, e := range r.current {
			if r.establishedOnlyLocked(e.route.Config) {
				continue
			}
			if p := r.rollovers[id]; p != nil && (p.state == "warming" || p.state == "paused" || p.state == "rolled_back" || !e.route.Instance.Ready.Load()) {
				for _, old := range r.entries {
					if old.route == p.old && r.permitted("", old.route.Config) {
						e = old
						break
					}
				}
			}
			if _, ok := p.rank[id]; !ok {
				continue
			}
			if e == nil || !e.route.Instance.Ready.Load() {
				continue
			}
			if !r.policyTargetReadyLocked(p.config, e.route.Config.ID, now) {
				continue
			}
			until := r.valid[id]
			if !until.IsZero() && now.After(until) {
				continue
			}
			candidates = append(candidates, e.route)
		}
		sort.SliceStable(candidates, func(i, j int) bool { return p.rank[candidates[i].Config.ID] < p.rank[candidates[j].Config.ID] })
		if p.config.Mode != "manual" && p.config.Mode != "fallback" && p.config.Mode != "round-robin" && p.config.Mode != "timed" {
			sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score(p.config.Mode) < candidates[j].score(p.config.Mode) })
		}
		limit := min(p.config.ActivePaths+p.config.WarmReserve, len(candidates))
		if p.config.Mode == "auto" || p.config.Mode == "throughput" {
			diverse := []*Route{}
			rest := []*Route{}
			groups := map[string]bool{}
			endpointGroups := r.endpointGroupsLocked(candidates, now)
			for index, v := range candidates {
				key := endpointGroups[index] + "/" + v.Config.EgressID
				if key != "" && groups[key] {
					p.topologyFactors[v.Config.ID] = "shared_balancer_deprioritized"
					rest = append(rest, v)
				} else {
					diverse = append(diverse, v)
					p.topologyFactors[v.Config.ID] = "distinct_server_ip_preferred"
					if len(r.routeIPsLocked(v, now)) == 0 {
						p.topologyFactors[v.Config.ID] = "dns_unknown"
					}
					groups[key] = true
				}
			}
			candidates = append(diverse, rest...)
		}
		if len(r.config.EgressPaths) > 0 && (p.config.Mode == "auto" || p.config.Mode == "throughput" || p.config.Mode == "round-robin") {
			first, rest := []*Route{}, []*Route{}
			seen := map[string]bool{}
			for _, v := range candidates {
				if !seen[v.Config.EgressID] {
					first = append(first, v)
					seen[v.Config.EgressID] = true
				} else {
					rest = append(rest, v)
				}
			}
			candidates = append(first, rest...)
		}
		p.best = append([]*Route(nil), candidates[:limit]...)
	}
}
func (r *Runtime) Candidates(key string) []*Route {
	return r.candidatesObserved(key, nil)
}
func (r *Runtime) candidatesObserved(key string, decision *topology.Decision) (result []*Route) {
	if decision != nil {
		decision.Policy = strings.TrimPrefix(key, "policy:")
		decision.Reason = "selected_by_policy"
		decision.Method = "ordered_candidates"
	}
	if r.drained.Load() || time.Now().Unix() > r.lease.Load() {
		if decision != nil {
			r.mu.RLock()
			decision.Generation = r.config.Generation
			r.mu.RUnlock()
			decision.Reason = "lease_expired"
		}
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if decision != nil {
		decision.Generation = r.config.Generation
	}
	out := []*Route{}
	var p *policyState
	defer func() {
		if decision != nil {
			r.captureCandidatesLocked(p, result, decision, now)
		}
	}()
	if strings.HasPrefix(key, "policy:") {
		p = r.policies[strings.TrimPrefix(key, "policy:")]
		if p == nil {
			return nil
		}
		for _, v := range p.best {
			until := r.valid[v.Config.ID]
			reason := ""
			if !r.permitted(p.config.ID, v.Config) {
				reason = "route_revoked"
			} else if !v.Instance.Ready.Load() {
				reason = "core_not_ready"
			} else if !until.IsZero() && !now.Before(until) {
				reason = "subscription_expired"
			} else if v.BlockedUntil.Load() > now.UnixNano() {
				reason = "circuit_open"
			}
			if reason == "" {
				out = append(out, v)
			} else if decision != nil && len(decision.Candidates) < 32 {
				decision.Candidates = append(decision.Candidates, candidateEvidence(v, nil, reason))
			} else if decision != nil {
				decision.Truncated = true
			}
		}
	} else {
		for id, e := range r.current {
			until := r.valid[id]
			if r.permitted("", e.route.Config) && (key == "" || key == id) && e.route.Instance.Ready.Load() && (until.IsZero() || now.Before(until)) {
				out = append(out, e.route)
			}
		}
	}
	if p != nil && p.config.Mode == "manual" {
		for _, v := range out {
			if v.Config.ID == p.config.Routes[0] {
				return []*Route{v}
			}
		}
		return nil
	}
	if len(out) < 2 {
		return out
	}
	n := r.cursor.Add(1)
	mode := "auto"
	if p != nil {
		mode = p.config.Mode
	}
	if decision != nil {
		decision.Mode = mode
	}
	if p != nil && (mode == "auto" || mode == "throughput") {
		return r.selectAutomaticLocked(p, out, decision)
	}
	if p != nil && len(r.config.EgressPaths) > 0 && (mode == "auto" || mode == "throughput" || mode == "round-robin") {
		return r.selectEgressLocked(p, out, decision)
	}
	switch mode {
	case "manual":
		if out[0].Config.ID != p.config.Routes[0] {
			return nil
		}
		return out[:1]
	case "fallback":
		return out
	case "round-robin":
		limit := min(len(out), max(1, p.config.ActivePaths))
		i := int(n-1) % limit
		return append(append(append([]*Route{}, out[i:limit]...), out[:i]...), out[limit:]...)
	case "timed":
		limit := min(len(out), max(1, p.config.ActivePaths))
		i := int(now.Unix()/int64(max(5, p.config.RotationSeconds))) % limit
		return append(append(append([]*Route{}, out[i:limit]...), out[:i]...), out[limit:]...)
	case "sticky":
		if p.chosen == "" || now.Sub(p.changed) > time.Duration(p.config.StickySeconds)*time.Second {
			p.chosen = out[0].Config.ID
			p.changed = now
		}
		for i, v := range out {
			if v.Config.ID == p.chosen {
				out[0], out[i] = out[i], out[0]
				break
			}
		}
		return out
	default:
		limit := len(out)
		if p != nil {
			limit = min(limit, p.config.ActivePaths)
		}
		if limit > 1 {
			a := int(n % uint64(limit))
			b := int((n*6364136223846793005 + 1442695040888963407) % uint64(limit))
			var evidenceA, evidenceB topology.Candidate
			var captureA, captureB *topology.Candidate
			if decision != nil {
				captureA, captureB = &evidenceA, &evidenceB
			}
			scoreB, scoreA := out[b].scoreObserved(mode, captureB), out[a].scoreObserved(mode, captureA)
			if decision != nil {
				decision.Method = "deterministic_two_choice"
				evidenceA.Score = &scoreA
				evidenceB.Score = &scoreB
				if len(decision.Candidates) > 30 {
					decision.Candidates = decision.Candidates[:30]
					decision.Truncated = true
				}
				decision.Candidates = append(decision.Candidates, evidenceA, evidenceB)
			}
			if scoreB < scoreA {
				a = b
			}
			out[0], out[a] = out[a], out[0]
		}
		return out
	}
}
func (r *Runtime) AdmitPolicy(key string) (func(), error) {
	id := strings.TrimPrefix(key, "policy:")
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.policies[id]
	if p == nil {
		return nil, fmt.Errorf("policy_unavailable")
	}
	if r.admission == nil {
		r.admission = map[string]*atomic.Int64{}
	}
	n := r.admission[id]
	if n == nil {
		n = &atomic.Int64{}
		r.admission[id] = n
	}
	limit := p.config.MaxConnections
	if limit <= 0 {
		limit = 5000
	}
	if n.Add(1) > int64(limit) {
		n.Add(-1)
		return nil, fmt.Errorf("policy_connection_budget_exhausted")
	}
	var once sync.Once
	return func() { once.Do(func() { n.Add(-1) }) }, nil
}
func (r *Runtime) RouteAllowed(key string, v config.Route) bool {
	if r.drained.Load() || time.Now().Unix() > r.lease.Load() {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	until, exists := r.valid[v.ID]
	e := r.current[v.ID]
	return exists && e != nil && e.route.Config.Core == v.Core && fingerprint(e.route.Config) == fingerprint(v) && (until.IsZero() || time.Now().Before(until)) && r.permitted(strings.TrimPrefix(key, "policy:"), v)
}
func (v *Route) score(mode string) float64 {
	return v.scoreObserved(mode, nil)
}

// One scoring implementation. Optional evidence records the exact operands read
// by this call, including the second load/error read in the capacity branch.
func (v *Route) scoreObserved(mode string, evidence *topology.Candidate) float64 {
	rtt := float64(v.RTTUS.Load()) / 1000
	if v.Instance != nil {
		rtt = max(rtt, float64(v.Instance.LatencyUS.Load())/1000)
	}
	fallbackRTT := rtt <= 0
	if fallbackRTT {
		rtt = 200
	}
	active, failures, errors := v.Active.Load(), v.ConsecutiveFailures.Load(), v.ErrorEWMA.Load()
	score := rtt * (1 + float64(active)*.25) * (1 + float64(failures))
	score *= 1 + float64(errors)/250
	speed := int64(0)
	probeAt := v.LastSpeed.Load()
	probeSpeed := int64(0)
	source := "none"
	formula := "latency_load_error"
	usedSpeed := int64(0)
	if time.Now().Unix()-probeAt < 900 {
		speed = v.SpeedBPS.Load()
		probeSpeed = speed
		if speed > 0 {
			source = "active_sample"
		}
	}
	// Delivered traffic is evidence of a lower bound, never an upper capacity claim.
	passiveAt := v.LastPassive.Load()
	if time.Now().Unix()-passiveAt < 30 {
		passive := v.PassiveBPS.Load()
		if passive > speed {
			source = "delivered_lower_bound"
		} else if passive > 0 && passive == speed {
			source = "active_or_passive_equal"
		}
		speed = max(speed, passive)
	}
	record := func() {
		if evidence == nil {
			return
		}
		*evidence = topology.Candidate{Egress: v.Config.EgressID, Route: v.Config.ID, Engine: v.Config.Core, Active: active, Reason: "compared", SpeedBPS: probeSpeed, MeasurementAt: probeAt, EffectiveRTTMS: rtt, ConsecutiveFailures: failures, ErrorEWMA: errors, SpeedUsedBPS: usedSpeed, SpeedSource: source, PassiveAt: passiveAt, CeilingBPS: v.CeilingBPS, Formula: formula}
		if fallbackRTT {
			evidence.Factors = append(evidence.Factors, "insufficient_observation")
		}
		if usedSpeed > 0 {
			factor := "measured_capacity"
			if source == "delivered_lower_bound" {
				factor = "passive_lower_bound_used"
			}
			evidence.Factors = append(evidence.Factors, factor)
		} else if probeAt > 0 && time.Now().Unix()-probeAt >= 900 {
			evidence.Factors = append(evidence.Factors, "measurement_stale")
		}
	}
	if mode != "latency" && speed > 0 {
		if v.CeilingBPS > 0 {
			speed = min(speed, v.CeilingBPS)
		}
		usedSpeed = speed
		if mode == "auto" || mode == "throughput" {
			// Estimate work per unit of measured capacity. Transient queueing in
			// a busy fast path must not send a large flow to a much slower path.
			latencyPenalty := 1 + min(rtt, 1000)/200
			if mode == "throughput" {
				latencyPenalty = 1 + min(rtt, 1000)/20000
			}
			active, failures, errors = v.Active.Load(), v.ConsecutiveFailures.Load(), v.ErrorEWMA.Load()
			formula = "capacity_weighted_load"
			score = (1 + float64(active)) * 12500000 / float64(speed) * latencyPenalty * (1 + float64(failures)) * (1 + float64(errors)/250)
			record()
			return score
		}
		score /= 1 + float64(speed)/12500000
		formula = "latency_load_error_speed_bonus"
	}
	record()
	return score
}
func (v *Route) success(d time.Duration) {
	old := v.RTTUS.Load()
	sample := d.Microseconds()
	if old == 0 {
		v.RTTUS.Store(sample)
	} else {
		v.RTTUS.Store((old*7 + sample) / 8)
	}
	v.Samples.Add(1)
	v.ErrorEWMA.Store(v.ErrorEWMA.Load() * 7 / 8)
	v.ConsecutiveFailures.Store(0)
	v.BlockedUntil.Store(0)
}
func (v *Route) failed() {
	// A cooldown alone cannot prove recovery. Remember the latest independent
	// successful-probe sequence observed at a local failure, without moving it
	// backwards when concurrent dials report errors out of order.
	if v.Instance != nil {
		proof := v.Instance.ProbeSamples.Load()
		for previous := v.FailureProbeSamples.Load(); proof > previous; previous = v.FailureProbeSamples.Load() {
			if v.FailureProbeSamples.CompareAndSwap(previous, proof) {
				break
			}
		}
	}
	n := v.ConsecutiveFailures.Add(1)
	if n >= 3 {
		v.BlockedUntil.Store(time.Now().Add(time.Duration(min(int64(60), 1<<min(n-3, 6))) * time.Second).UnixNano())
	}
}
func (r *Runtime) background() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			r.mu.Lock()
			entries := []*engineEntry{}
			for _, e := range r.entries {
				e.cancel()
				entries = append(entries, e)
			}
			r.mu.Unlock()
			for _, e := range entries {
				e.manager.Wait()
			}
			return
		case <-t.C:
			r.refreshEndpointIPs(r.ctx)
			r.refreshServerPins(r.ctx)
			r.refreshTargetHealth(r.ctx)
			r.maintain()
		}
	}
}
func (r *Runtime) maintain() {
	if !r.applyMu.TryLock() {
		return
	}
	defer r.applyMu.Unlock()
	r.mu.Lock()
	r.rebuildLocked()
	cfg := r.config
	keep := map[string]bool{}
	for _, p := range r.policies {
		for _, v := range p.best {
			if e := r.current[v.Config.ID]; e != nil {
				keep[e.key] = true
				e.used = time.Now()
			}
		}
	}
	var retire []*engineEntry
	for _, p := range r.rollovers {
		if !r.establishedOnlyLocked(p.next.Config) && (p.state != "active_draining" || (p.next.Config.UDP && !udpProven(p.next))) && r.overlapAllowedLocked("", p.old.Config) {
			for key, e := range r.entries {
				if e.route == p.old || e.route == p.next {
					keep[key] = true
					e.used = time.Now()
				}
			}
		}
	}
	for k, e := range r.entries {
		if !keep[k] && e.route.Active.Load() == 0 && e.route.References.Load() == 0 && time.Since(e.used) > 30*time.Second {
			r.retiredUp.Add(e.route.BytesUp.Load())
			r.retiredDown.Add(e.route.BytesDown.Load())
			delete(r.entries, k)
			delete(r.staging, k)
			if r.current[e.route.Config.ID] == e {
				delete(r.current, e.route.Config.ID)
			}
			retire = append(retire, e)
		}
	}
	r.mu.Unlock()
	for _, e := range retire {
		e.cancel()
		e.manager.Wait()
		os.RemoveAll(filepath.Join(r.rootDir, e.key))
		r.mu.Lock()
		delete(r.slots, e.slot)
		r.mu.Unlock()
	}
	if len(cfg.Routes) == 0 {
		return
	}
	if r.drained.Load() || time.Now().Unix() > r.lease.Load() {
		return
	}
	// Fill hot paths first, then visit a cold profile at a bounded rate.
	r.mu.RLock()
	count := len(r.current)
	r.mu.RUnlock()
	for range cfg.Routes {
		r.coldCursor = (r.coldCursor + 1) % len(cfg.Routes)
		v := cfg.Routes[r.coldCursor]
		r.mu.RLock()
		e := r.current[v.ID]
		r.mu.RUnlock()
		if e != nil && fingerprint(e.route.Config) == fingerprint(v) {
			continue
		}
		if e == nil && count >= hotLimit+2 {
			continue
		}
		delay := 5 * time.Minute
		if e != nil {
			delay = 30 * time.Second
		}
		if v.EstablishedOnly || time.Since(r.visited[v.ID]) < delay || (!v.ValidUntil.IsZero() && time.Now().After(v.ValidUntil)) {
			continue
		}
		r.visited[v.ID] = time.Now()
		entry, err := r.ensure(r.ctx, cfg, v)
		if err == nil {
			r.mu.Lock()
			entry.staged = false
			r.current[v.ID] = entry
			if e != nil {
				r.installRolloversLocked(map[string]*engineEntry{v.ID: e}, time.Now())
			}
			r.mu.Unlock()
			if len(r.restoreRollovers) > 0 {
				recovered := r.restoreRolloverEngines(r.ctx, cfg)
				r.mu.Lock()
				r.installRecoveredRolloversLocked(recovered)
				r.mu.Unlock()
			}
		}
		return
	}
}
func (r *Runtime) Restart(id string) error {
	r.mu.RLock()
	e := r.entries[id]
	if e == nil {
		e = r.current[id]
	}
	r.mu.RUnlock()
	if e == nil {
		return fmt.Errorf("engine_not_running")
	}
	pid := int(e.route.Instance.PID.Load())
	if pid <= 1 {
		return fmt.Errorf("engine_not_running")
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return fmt.Errorf("engine_not_running")
	}
	defer unix.Close(fd)
	command, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || !strings.Contains(string(command), filepath.Join(r.rootDir, e.key)+"/") {
		return fmt.Errorf("engine_identity_changed")
	}
	if unix.PidfdSendSignal(fd, syscall.SIGKILL, nil, 0) != nil {
		return fmt.Errorf("engine_signal_failed")
	}
	return nil
}
func (g *Gateway) closeIdle() {
	g.listenerMu.Lock()
	defer g.listenerMu.Unlock()
	for _, b := range g.bound {
		if b.transport != nil {
			b.transport.CloseIdleConnections()
		}
	}
	for _, t := range g.transports {
		t.CloseIdleConnections()
	}
}
func (r *Runtime) JSONStatus(g *Gateway) json.RawMessage {
	b, _ := json.Marshal(r.Status(g))
	return b
}
