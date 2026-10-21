package worker

import (
	"bytes"
	"fmt"
	"io"
	"runtime"
	"time"
)

func (r *Runtime) entryReadyLocked(e *engineEntry) bool {
	v := e.route
	if r.current[v.Config.ID] != e || !v.Instance.Ready.Load() || r.drained.Load() || time.Now().Unix() > r.lease.Load() {
		return false
	}
	until := r.valid[v.Config.ID]
	if !until.IsZero() && time.Now().After(until) {
		return false
	}
	for _, p := range r.policies {
		if !r.policyTargetReadyLocked(p.config, v.Config.ID, time.Now()) {
			continue
		}
		if p.config.Mode == "manual" && (len(p.config.Routes) == 0 || p.config.Routes[0] != v.Config.ID) {
			continue
		}
		if !r.permitted(p.config.ID, v.Config) {
			continue
		}
		for _, path := range p.best {
			if path == v {
				return true
			}
		}
	}
	return false
}

// Reservations are durable across restarts; probe_bytes is a per-process
// Prometheus counter of bodies actually read. Do not confuse the two.
func (r *Runtime) reservedProbeBudgetLocked() int64 {
	if !time.Now().UTC().Truncate(24 * time.Hour).Equal(r.probeWindow.UTC().Truncate(24 * time.Hour)) {
		return 0
	}
	var used int64
	for _, v := range r.budget {
		used += v
	}
	return used
}
func (r *Runtime) Status(g *Gateway) map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	engines := []map[string]any{}
	up, down := r.retiredUp.Load(), r.retiredDown.Load()
	for _, e := range r.entries {
		v := e.route
		up += v.BytesUp.Load()
		down += v.BytesDown.Load()
		state := "ready"
		if r.current[v.Config.ID] != e {
			state = "draining"
		} else if !v.Instance.Ready.Load() {
			state = "probe_failed"
			if v.Instance.LastProbe.Load() == 0 {
				state = "starting"
			}
		} else if !r.entryReadyLocked(e) {
			state = "reserve_or_blocked"
		}
		engines = append(engines, map[string]any{"id": e.key, "profile_id": v.Config.Profile(), "route_id": v.Config.ID, "egress": v.Config.EgressID, "core": v.Config.Core, "version": v.Instance.Config.Version, "sha256": v.Instance.SHA256, "pid": v.Instance.PID.Load(), "ready": r.entryReadyLocked(e), "healthy": v.Instance.Ready.Load(), "state": state, "starts": v.Instance.Starts.Load(), "restarts": v.Instance.Restarts.Load(), "connections": v.Connections.Load(), "active": v.Active.Load(), "failures": v.Failures.Load(), "health_requests": v.Instance.HealthRequests.Load(), "rtt_ms": float64(v.RTTUS.Load()) / 1000, "probe_rtt_ms": float64(v.Instance.LatencyUS.Load()) / 1000, "probe_samples": v.Instance.ProbeSamples.Load(), "last_probe_at": v.Instance.LastProbe.Load(), "samples": v.Samples.Load(), "speed_bps": v.SpeedBPS.Load(), "speed_sample_at": v.LastSpeed.Load(), "speed_confidence": "bounded_active_sample", "passive_goodput_bps": v.PassiveBPS.Load(), "passive_capacity_claim": false, "circuit_until": v.BlockedUntil.Load(), "bytes_up": v.BytesUp.Load(), "bytes_down": v.BytesDown.Load()})
		engines[len(engines)-1]["server_ips"] = r.routeIPsLocked(v, time.Now())
		engines[len(engines)-1]["server_ips_observed_at"] = r.endpointIPs[endpointHost(v)].Observed
	}
	policies := []map[string]any{}
	for id, p := range r.policies {
		paths := []string{}
		for _, v := range p.best {
			e := r.current[v.Config.ID]
			if e != nil && r.entryReadyLocked(e) && r.policyTargetReadyLocked(p.config, v.Config.ID, time.Now()) {
				paths = append(paths, v.Config.ID)
			}
		}
		used := r.budget[id]
		if !time.Now().Before(r.probeWindow.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)) {
			used = 0
		}
		policies = append(policies, map[string]any{"id": id, "mode": p.config.Mode, "ready_paths": paths, "catalog_size": len(p.config.Routes), "probe_budget_remaining_bytes": max(int64(0), p.config.ProbeBudgetBytes-used)})
		policies[len(policies)-1]["probe_scope"] = p.config.ProbeScope
	}
	return map[string]any{"egress_paths": r.config.EgressPaths, "architecture": runtime.GOARCH, "cores": engines, "routes": engines, "policies": policies, "catalog_size": len(r.config.Routes), "active_connections": g.active.Load(), "udp_associations": g.udpActive.Load(), "udp_drops": g.udpDrops.Load(), "udp_packets": g.udpPackets.Load(), "drained": r.drained.Load(), "lease_until": r.lease.Load(), "probe_bytes": r.probeBytes.Load(), "probe_budget_reserved_bytes": r.reservedProbeBudgetLocked(), "probe_budget_day": r.probeWindow.UTC().Truncate(24 * time.Hour).Format("2006-01-02"), "payload_bytes_up": up, "payload_bytes_down": down}
}
func (r *Runtime) Metrics(output io.Writer) {
	var buffer bytes.Buffer
	processes := map[string]int64{}
	func() {
		r.mu.RLock()
		defer r.mu.RUnlock()
		w := &buffer
		ready := 0
		for _, p := range r.config.EgressPaths {
			enabled := 0
			if p.Enabled {
				enabled = 1
			}
			fmt.Fprintf(w, "proxy_isp_enabled{isp=%q} %d\nproxy_isp_weight{isp=%q} %d\nproxy_isp_download_limit_bytes_per_second{isp=%q} %d\n", p.ID, enabled, p.ID, p.Weight, p.ID, p.DownloadMbps*125000)
		}
		up, down := r.retiredUp.Load(), r.retiredDown.Load()
		fmt.Fprintf(w, "proxy_engine_processes %d\nproxy_catalog_profiles %d\nproxy_offline_lease_until_seconds %d\nproxy_probe_bytes_total %d\n", len(r.entries), len(r.config.Routes), r.lease.Load(), r.probeBytes.Load())
		fmt.Fprintf(w, "proxy_probe_budget_reserved_bytes %d\n", r.reservedProbeBudgetLocked())
		fmt.Fprintf(w, "proxy_worker_goroutines %d\n", runtime.NumGoroutine())
		for _, e := range r.entries {
			v := e.route
			processes[e.key] = v.Instance.PID.Load()
			fmt.Fprintf(w, "proxy_route_egress_info{route=%q,engine=%q,profile=%q,isp=%q} 1\n", v.Config.ID, e.key, v.Config.Profile(), v.Config.EgressID)
			healthy := 0
			if v.Instance.Ready.Load() {
				healthy = 1
			}
			if r.entryReadyLocked(e) {
				ready++
			}
			up += v.BytesUp.Load()
			down += v.BytesDown.Load()
			fmt.Fprintf(w, "proxy_path_health_requests_total{route=%q,engine=%q} %d\n", v.Config.ID, e.key, v.Instance.HealthRequests.Load())
			fmt.Fprintf(w, "proxy_core_ready{core=%q,version=%q} %d\nproxy_core_restarts_total{core=%q} %d\nproxy_route_connections_total{route=%q,core=%q,engine=%q} %d\nproxy_route_failures_total{route=%q,engine=%q} %d\nproxy_route_rtt_seconds{route=%q,engine=%q} %g\nproxy_traffic_bytes_total{route=%q,engine=%q,direction=\"up\"} %d\nproxy_traffic_bytes_total{route=%q,engine=%q,direction=\"down\"} %d\nproxy_path_probe_duration_seconds{route=%q,engine=%q} %g\nproxy_path_goodput_bytes_per_second{route=%q,engine=%q,measurement=\"active\"} %d\n", e.key, v.Instance.Config.Version, healthy, e.key, v.Instance.Restarts.Load(), v.Config.ID, v.Config.Core, e.key, v.Connections.Load(), v.Config.ID, e.key, v.Failures.Load(), v.Config.ID, e.key, float64(v.RTTUS.Load())/1e6, v.Config.ID, e.key, v.BytesUp.Load(), v.Config.ID, e.key, v.BytesDown.Load(), v.Config.ID, e.key, float64(v.Instance.LatencyUS.Load())/1e6, v.Config.ID, e.key, v.SpeedBPS.Load())
		}
		fmt.Fprintf(w, "proxy_paths_ready %d\nproxy_payload_bytes_total{direction=\"up\"} %d\nproxy_payload_bytes_total{direction=\"down\"} %d\n", ready, up, down)
		for id, p := range r.policies {
			used := r.budget[id]
			if !time.Now().Before(r.probeWindow.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)) {
				used = 0
			}
			fmt.Fprintf(w, "proxy_probe_budget_remaining_bytes{policy=%q} %d\n", id, max(int64(0), p.config.ProbeBudgetBytes-used))
		}
	}()
	for key, pid := range processes {
		engineResources(&buffer, key, pid)
	}
	output.Write(buffer.Bytes())
}
