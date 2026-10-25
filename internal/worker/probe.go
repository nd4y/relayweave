package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/wire"
	"sort"
	"strconv"
	"time"
)

type profileProbe struct {
	Route     config.Route    `json:"route"`
	Core      config.Core     `json:"core"`
	Artifacts []core.Artifact `json:"artifacts"`
	ProbeURL  string          `json:"probe_url"`
	SpeedURL  string          `json:"speed_url"`
	PolicyID  string          `json:"policy_id"`
	Budget    int64           `json:"budget"`
	Automatic bool            `json:"automatic,omitempty"`
}

func (a *Agent) test(ctx context.Context, command wire.Command) (any, error) {
	a.probeMu.Lock()
	defer a.probeMu.Unlock()
	var body profileProbe
	if len(command.Payload) > 0 && json.Unmarshal(command.Payload, &body) != nil {
		return nil, fmt.Errorf("probe_payload_invalid")
	}
	r := a.runtime
	if time.Now().Unix() > r.lease.Load() {
		return nil, fmt.Errorf("profile_lease_expired")
	}
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	cfg := r.Configuration()
	var route config.Route
	for _, v := range cfg.Routes {
		if v.ID == command.Target {
			route = v
			break
		}
	}
	if route.ID == "" {
		if body.Route.ID != command.Target || body.Core.ID == "" {
			return nil, fmt.Errorf("profile_not_in_configuration")
		}
		route = body.Route
		cfg = config.Config{Cores: []config.Core{body.Core}, Routes: []config.Route{route}, ProbeURL: body.ProbeURL, ConnectTimeoutMS: 3000, ProbeIntervalSeconds: 30}
		if err := a.install(ctx, cfg, body.Artifacts); err != nil {
			return nil, fmt.Errorf("probe_artifact_unavailable")
		}
	}
	if !route.ValidUntil.IsZero() && !time.Now().Before(route.ValidUntil) {
		return nil, fmt.Errorf("profile_lease_expired")
	}
	if body.ProbeURL == "" {
		body.ProbeURL = cfg.ProbeURL
	}
	if body.ProbeURL == "" {
		return nil, fmt.Errorf("probe_url_required")
	}
	cfg.ProbeURL = body.ProbeURL
	e, err := r.ensure(ctx, cfg, route)
	if err != nil {
		return nil, err
	}
	if command.Action == "speed-test" && body.Automatic && e.route.probeTargetCooling(time.Now()) {
		return nil, fmt.Errorf("probe_target_cooldown")
	}
	e.route.LastProbeAttempt.Store(time.Now().Unix())
	targetURL := body.ProbeURL
	limit := int64(65536)
	if command.Action == "speed-test" {
		if body.SpeedURL == "" {
			return nil, fmt.Errorf("speed_url_required")
		}
		targetURL = body.SpeedURL
		limit = 8 << 20
		if body.Automatic {
			limit = automaticCapacityProbeBytes
		}
		budget := body.Budget
		if budget <= 0 {
			budget = 128 << 20
		}
		key := body.PolicyID
		if key == "" {
			key = "manual"
		}
		if !r.reserveCapacityProbe(time.Now(), key, limit, budget, body.Automatic) {
			return nil, fmt.Errorf("probe_budget_exhausted")
		}
		// Reserve on durable storage before sending a byte, including after restart.
		a.mu.Lock()
		e := a.saveLocked()
		a.mu.Unlock()
		if e != nil {
			return nil, fmt.Errorf("probe_budget_persistence_failed")
		}
	}
	tr := &http.Transport{DisableKeepAlives: true, DisableCompression: true, ResponseHeaderTimeout: 8 * time.Second, DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		return e.route.Instance.Adapter().OpenTCP(ctx, e.route.Config.Endpoint, addr, 3*time.Second)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 25 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	// Wait for the private listener, independently of a profile's health endpoint.
	for {
		c, err := net.DialTimeout("tcp", e.route.Config.Endpoint, 100*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("probe_timeout")
		case <-time.After(100 * time.Millisecond):
		}
	}
	started := time.Now()
	if command.Action == "speed-test" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
	}
	request, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("probe_url_invalid")
	}
	request.Header.Set("Cache-Control", "no-cache")
	if command.Action == "speed-test" {
		request.Header.Set("Range", fmt.Sprintf("bytes=0-%d", limit-1))
	}
	resp, err := client.Do(request)
	if err != nil {
		// An optional capacity origin is not the route's health authority.
		if command.Action != "speed-test" {
			e.route.failed()
		}
		return nil, fmt.Errorf("probe_connection_failed")
	}
	defer resp.Body.Close()
	first := time.Since(started)
	if command.Action == "speed-test" {
		e.route.ProbeHTTPStatus.Store(int64(resp.StatusCode))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			e.route.ProbeNotBefore.Store(probeRetryAt(resp.Header.Get("Retry-After"), time.Now()).Unix())
			return nil, fmt.Errorf("probe_target_throttled")
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("probe_http_status")
	}
	var trafficBefore int64
	if command.Action == "speed-test" {
		trafficBefore = a.gateway.routeDownSnapshot(e.route)
	}
	bodyStarted := time.Now()
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, limit))
	var trafficAfter int64
	if command.Action == "speed-test" {
		trafficAfter = a.gateway.routeDownSnapshot(e.route)
	}
	bodyTime := time.Since(bodyStarted)
	elapsed := time.Since(started)
	r.probeBytes.Add(n)
	if err != nil && (command.Action != "speed-test" || n < 1<<20) {
		return nil, fmt.Errorf("probe_body_failed")
	}
	e.route.success(first)
	result := map[string]any{"healthy": true, "status_code": resp.StatusCode, "ttfb_ms": float64(first.Microseconds()) / 1000, "elapsed_ms": float64(elapsed.Microseconds()) / 1000, "bytes": n, "origin": "active_probe", "completed_at": time.Now().UTC()}
	if command.Action == "speed-test" {
		if n < 1<<20 {
			return nil, fmt.Errorf("speed_sample_too_small")
		}
		// Setup/TTFB is scored separately as latency. Including it in a short
		// transfer sample systematically understates fast, distant paths.
		if bodyTime <= 0 {
			return nil, fmt.Errorf("speed_sample_duration_invalid")
		}
		// A probe competes with existing transfers. Measure their delivered
		// bytes on this exact engine in the SAME window, otherwise a busy fast
		// path is mistaken for a slow one. This adds no measurement traffic.
		concurrentBytes := max(int64(0), trafficAfter-trafficBefore)
		bps := int64(float64(n+concurrentBytes) / bodyTime.Seconds())
		e.route.SpeedBPS.Store(bps)
		e.route.LastSpeed.Store(time.Now().Unix())
		e.route.CapacityConnections.Store(e.route.Connections.Load())
		e.route.ProbeNotBefore.Store(0)
		e.route.ProbeRequestedAt.Store(0)
		r.mu.Lock()
		r.rememberCapacityLocked(e, cfg, targetURL, bps, time.Now())
		r.mu.Unlock()
		a.mu.Lock()
		persistErr := a.saveLocked()
		a.mu.Unlock()
		if persistErr != nil {
			return nil, fmt.Errorf("capacity_sample_persistence_failed")
		}
		result["mbps"] = float64(bps) * 8 / 1e6
		result["body_ms"] = float64(bodyTime.Microseconds()) / 1000
		result["probe_mbps"] = float64(n) * 8 / bodyTime.Seconds() / 1e6
		result["concurrent_bytes"] = concurrentBytes
		result["confidence"] = "single_bounded_aggregate_sample"
	}
	return result, nil
}

// Bound provider-origin backoff; never refund a durable byte reservation or
// turn a throttled benchmark origin into a route health/capacity failure.
func probeRetryAt(value string, now time.Time) time.Time {
	delay := 10 * time.Minute
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
		delay = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(value); err == nil {
		delay = at.Sub(now)
	}
	return now.Add(max(time.Minute, min(time.Hour, delay)))
}

func (r *Route) probeTargetCooling(now time.Time) bool {
	status := r.ProbeHTTPStatus.Load()
	return (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable) && r.ProbeNotBefore.Load() > now.Unix()
}

func (r *Runtime) reserveProbe(now time.Time, key string, size, budget int64) bool {
	if size <= 0 || budget <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reserveProbeLocked(now, key, size, budget)
}

func (r *Runtime) reserveProbeLocked(now time.Time, key string, size, budget int64) bool {
	return r.reserveClassifiedProbeLocked(now, key, size, budget, false, false)
}

func (r *Runtime) reserveCapacityProbe(now time.Time, key string, size, budget int64, automatic bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reserveClassifiedProbeLocked(now, key, size, budget, true, automatic)
}

func (r *Runtime) reserveClassifiedProbeLocked(now time.Time, key string, size, budget int64, capacity, automatic bool) bool {
	if size <= 0 || budget <= 0 {
		return false
	}
	// A reservation captured before Restrict must respect the current lower cap.
	policies := r.config.Policies
	if r.constraints != nil {
		policies = r.constraints.Policies
	}
	for _, p := range policies {
		if p.ID == key {
			budget = min(budget, p.ProbeBudgetBytes)
		}
	}
	if r.probeWindow.IsZero() || !now.Before(r.probeWindow.UTC().Truncate(24*time.Hour).Add(24*time.Hour)) {
		r.probeWindow = now.UTC().Truncate(24 * time.Hour)
		r.budget = map[string]int64{}
		r.capacityBudget = map[string]int64{}
	}
	if r.budget == nil {
		r.budget = map[string]int64{}
	}
	ceiling := r.probeCeilingLocked(budget)
	var total int64
	for _, n := range r.budget {
		total += n
	}
	if size > budget || r.budget[key] > budget-size || size > ceiling || total > ceiling-size {
		return false
	}
	if capacity && automatic {
		// Only capacity reservations consume the capacity half. Quality/UDP
		// reservations still count against the SAME unmodified global ceiling.
		if size > ceiling/2 || r.capacityReservedLocked(now) > ceiling/2-size {
			return false
		}
	}
	if r.capacityBudget == nil {
		// Old journals have no purpose breakdown. Never refund their spending.
		r.capacityBudget = cloneProbeBudget(r.budget)
	}
	r.budget[key] += size
	if capacity {
		r.capacityBudget[key] += size
	}
	return true
}

// Inspect already running, eligible engines without consuming client-selection
// credits. Restricting discovery to p.best makes capacity-based admission circular:
// an unmeasured, slightly slower-to-connect reserve can never prove it is faster.
func (r *Runtime) capacityProbeCandidates(policy string) []*Route {
	now := time.Now()
	if r.drained.Load() || now.Unix() > r.lease.Load() {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := r.policies[policy]
	if p == nil {
		return nil
	}
	out := []*Route{}
	for id, e := range r.current {
		if _, ok := p.rank[id]; !ok || e == nil || e.staged {
			continue
		}
		v := e.route
		if v == nil || v.Instance == nil || !v.Instance.Ready.Load() || r.establishedOnlyLocked(v.Config) || !r.permitted(policy, v.Config) || !r.egressEnabledLocked(v.Config.EgressID) {
			continue
		}
		if transition := r.rollovers[id]; transition != nil && transition.state != "active_draining" && transition.state != "completed" {
			continue // Capacity traffic must not bypass subscription canary admission.
		}
		if until := r.valid[id]; !until.IsZero() && !now.Before(until) {
			continue
		}
		if v.BlockedUntil.Load() > now.UnixNano() || r.telemetryDownLocked(v.Config.EgressID, now) || !r.policyTargetReadyLocked(p.config, id, now) {
			continue
		}
		if r.localV1 && p.config.Mode == "auto" && !r.localEgressAllowed(v.Config.EgressID) {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return p.rank[out[i].Config.ID] < p.rank[out[j].Config.ID] })
	return out
}

func (a *Agent) adaptiveProbes() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var previousBytes int64
	previousAt := time.Now()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			cfg := a.runtime.Configuration()
			now := time.Now()
			a.runtime.mu.RLock()
			total := a.runtime.retiredDown.Load()
			for _, e := range a.runtime.entries {
				total += e.route.BytesDown.Load()
			}
			a.runtime.mu.RUnlock()
			rate := float64(max(0, total-previousBytes)) / now.Sub(previousAt).Seconds()
			previousBytes, previousAt = total, now
			// Do not measure upstream capacity while the home access link is saturated.
			if cfg.DownloadMbps > 0 && rate > float64(cfg.DownloadMbps)*125000*.85 {
				continue
			}
			type candidate struct {
				policy    config.Policy
				route     *Route
				requested int64
				attempt   int64
			}
			choices := []candidate{}
			seen := map[string]bool{}
			for _, p := range cfg.Policies {
				if p.DisableSpeedProbes || (p.Mode != "auto" && p.Mode != "throughput") {
					continue
				}
				routes := a.runtime.capacityProbeCandidates(p.ID)
				if len(routes) < 2 {
					continue // No capacity choice to improve.
				}
				// Capacity reservations have their own half-share inside the
				// unchanged global cap, independently of quality/UDP spending.
				a.runtime.mu.RLock()
				reserved := a.runtime.capacityReservedLocked(now)
				ceiling := a.runtime.probeCeilingLocked(p.ProbeBudgetBytes)
				a.runtime.mu.RUnlock()
				if reserved+automaticCapacityProbeBytes > ceiling/2 {
					continue
				}
				if p.SpeedURL != "" && p.ProbeBudgetBytes >= automaticCapacityProbeBytes {
					for _, route := range routes {
						attempt, requested := route.LastProbeAttempt.Load(), route.ProbeRequestedAt.Load()
						if seen[route.Config.ID] || now.Unix()-attempt < 30 || now.Unix() < route.ProbeNotBefore.Load() {
							continue
						}
						if !route.capacityRefreshUseful(now) {
							continue
						}
						seen[route.Config.ID] = true
						choices = append(choices, candidate{p, route, requested, attempt})
					}
				}
			}
			if len(choices) == 0 {
				continue
			}
			sort.SliceStable(choices, func(i, j int) bool {
				a, b := choices[i], choices[j]
				if (a.requested != 0) != (b.requested != 0) {
					return a.requested != 0
				}
				return a.attempt < b.attempt
			})
			p, route := choices[0].policy, choices[0].route
			since := route.ProbeRequestedAt.Swap(0)
			if since == 0 {
				since = time.Now().Unix()
			}
			previous := route.SpeedBPS.Load()
			body, _ := json.Marshal(profileProbe{ProbeURL: p.ProbeURL, SpeedURL: p.SpeedURL, PolicyID: p.ID, Budget: p.ProbeBudgetBytes, Automatic: true})
			ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
			_, err := a.test(ctx, wire.Command{Action: "speed-test", Target: route.Config.ID, Payload: body})
			cancel()
			if err == nil {
				a.runtime.capacityConfirmed(p.ID, route, previous, since)
			}
		}
	}
}
