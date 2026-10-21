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
}

func (a *Agent) test(ctx context.Context, command wire.Command) (any, error) {
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
	e.route.LastProbeAttempt.Store(time.Now().Unix())
	targetURL := body.ProbeURL
	limit := int64(65536)
	if command.Action == "speed-test" {
		if body.SpeedURL == "" {
			return nil, fmt.Errorf("speed_url_required")
		}
		targetURL = body.SpeedURL
		limit = 8 << 20
		budget := body.Budget
		if budget <= 0 {
			budget = 128 << 20
		}
		key := body.PolicyID
		if key == "" {
			key = "manual"
		}
		if !r.reserveProbe(time.Now(), key, limit, budget) {
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
		e.route.failed()
		return nil, fmt.Errorf("probe_connection_failed")
	}
	defer resp.Body.Close()
	first := time.Since(started)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("probe_http_status")
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, limit))
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
		bps := int64(float64(n) / elapsed.Seconds())
		e.route.SpeedBPS.Store(bps)
		e.route.LastSpeed.Store(time.Now().Unix())
		e.route.ProbeNotBefore.Store(0)
		e.route.ProbeRequestedAt.Store(0)
		result["mbps"] = float64(bps) * 8 / 1e6
		result["confidence"] = "single_bounded_sample"
	}
	return result, nil
}

func (r *Runtime) reserveProbe(now time.Time, key string, size, budget int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.probeWindow.IsZero() || !now.Before(r.probeWindow.UTC().Truncate(24*time.Hour).Add(24*time.Hour)) {
		r.probeWindow = now.UTC().Truncate(24 * time.Hour)
		r.budget = map[string]int64{}
	}
	if r.budget == nil {
		r.budget = map[string]int64{}
	}
	ceiling := budget
	for _, p := range r.config.Policies {
		ceiling = max(ceiling, p.ProbeBudgetBytes)
	}
	var total int64
	for _, n := range r.budget {
		total += n
	}
	if r.budget[key]+size > budget || total+size > ceiling {
		return false
	}
	r.budget[key] += size
	return true
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
				if p.SpeedURL != "" && p.ProbeBudgetBytes >= 8<<20 {
					for _, route := range a.runtime.Candidates("policy:" + p.ID) {
						attempt, requested := route.LastProbeAttempt.Load(), route.ProbeRequestedAt.Load()
						if seen[route.Config.ID] || now.Unix()-attempt < 30 || now.Unix() < route.ProbeNotBefore.Load() {
							continue
						}
						if requested == 0 && now.Unix()-route.LastSpeed.Load() < capacityRefreshSeconds {
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
			body, _ := json.Marshal(profileProbe{ProbeURL: p.ProbeURL, SpeedURL: p.SpeedURL, PolicyID: p.ID, Budget: p.ProbeBudgetBytes})
			ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
			_, err := a.test(ctx, wire.Command{Action: "speed-test", Target: route.Config.ID, Payload: body})
			cancel()
			if err == nil {
				a.runtime.capacityConfirmed(p.ID, route, previous, since)
			}
		}
	}
}
