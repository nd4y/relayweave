package worker

import (
	"context"
	"proxyplatform/internal/config"
	"sort"
	"strings"
	"sync"
	"time"
)

// A generic successful probe must not admit a route into another policy whose
// application target is blocked. Keep target health separate from core health:
// a failed site check must not quarantine a core usable by other policies.
type targetObservation struct {
	Signature string
	Checked   time.Time
	Healthy   bool
}

func targetSignature(p config.Policy) string {
	return p.ProbeURL + "\x00" + strings.Join(p.ProbeFallbackURLs, "\x00")
}

func (r *Runtime) targetScoped(p config.Policy) bool {
	if p.ProbeScope == "shared" {
		return false
	}
	if p.ProbeScope == "target" {
		return p.ProbeURL != ""
	}
	if p.ProbeURL == "" {
		return false
	}
	if p.ProbeURL != r.config.ProbeURL {
		return true
	}
	for _, other := range r.policies {
		if other.config.ProbeURL != "" && targetSignature(other.config) != targetSignature(p) {
			return true
		}
	}
	return false
}

func (r *Runtime) policyTargetReadyLocked(p config.Policy, id string, now time.Time) bool {
	if !r.targetScoped(p) {
		return true
	}
	observation := r.targetHealth[p.ID+"/"+id]
	maxAge := max(60, 3*p.ProbeIntervalSeconds)
	return observation.Healthy && observation.Signature == targetSignature(p) && now.Sub(observation.Checked) <= time.Duration(maxAge)*time.Second
}

func (r *Runtime) installTargetObservationsLocked(validated map[string]targetObservation) {
	if r.targetHealth == nil {
		r.targetHealth = map[string]targetObservation{}
	}
	keep := map[string]bool{}
	for _, p := range r.policies {
		if !r.targetScoped(p.config) {
			continue
		}
		for _, id := range p.config.Routes {
			keep[p.config.ID+"/"+id] = true
		}
	}
	for key := range r.targetHealth {
		if !keep[key] {
			delete(r.targetHealth, key)
		}
	}
	for key, value := range validated {
		if keep[key] {
			r.targetHealth[key] = value
		}
	}
}

func (r *Runtime) refreshTargetHealth(parent context.Context) {
	type check struct {
		key     string
		policy  config.Policy
		entry   *engineEntry
		checked time.Time
	}
	now := time.Now()
	checks := []check{}
	r.mu.RLock()
	for _, p := range r.policies {
		if !r.targetScoped(p.config) {
			continue
		}
		for id, e := range r.current {
			if _, ok := p.rank[id]; !ok || !e.route.Instance.Ready.Load() {
				continue
			}
			if until := r.valid[id]; !until.IsZero() && now.After(until) {
				continue
			}
			key := p.config.ID + "/" + id
			old := r.targetHealth[key]
			if old.Signature == targetSignature(p.config) && now.Sub(old.Checked) < time.Duration(max(10, p.config.ProbeIntervalSeconds))*time.Second {
				continue
			}
			checks = append(checks, check{key, p.config, e, old.Checked})
		}
	}
	r.mu.RUnlock()
	if len(checks) == 0 {
		return
	}
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].checked.Equal(checks[j].checked) {
			return checks[i].key < checks[j].key
		}
		return checks[i].checked.Before(checks[j].checked)
	})
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	jobs := make(chan check, len(checks))
	for _, v := range checks {
		jobs <- v
	}
	close(jobs)
	var wg sync.WaitGroup
	for range min(4, len(checks)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for v := range jobs {
				if ctx.Err() != nil {
					return
				}
				cfg := config.Config{ProbeURL: v.policy.ProbeURL, ProbeFallbackURLs: v.policy.ProbeFallbackURLs,
					ConnectTimeoutMS: max(100, min(3000, v.policy.ConnectTimeoutMS))}
				healthy := v.entry.route.Instance.CheckRoute(ctx, v.entry.route.Config.Endpoint, cfg)
				// A saturated/cancelled batch is not evidence that unvisited routes failed.
				if ctx.Err() != nil {
					continue
				}
				r.mu.Lock()
				p := r.policies[v.policy.ID]
				if p != nil && targetSignature(p.config) == targetSignature(v.policy) && r.current[v.entry.route.Config.ID] == v.entry {
					if r.targetHealth == nil {
						r.targetHealth = map[string]targetObservation{}
					}
					r.targetHealth[v.key] = targetObservation{Signature: targetSignature(v.policy), Checked: time.Now(), Healthy: healthy}
				}
				r.mu.Unlock()
			}
		}()
	}
	wg.Wait()
	r.mu.Lock()
	r.rebuildLocked()
	r.mu.Unlock()
}
