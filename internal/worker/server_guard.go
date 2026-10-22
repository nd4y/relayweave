package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/config"
	"time"
)

// Pin the actual core socket, not just a preflight DNS observation. Keep the
// provider hostname as TLS identity and HTTP authority when it was implicit.
func checkedServer(ctx context.Context, route config.Route) (config.Route, string, error) {
	if !route.CheckServer {
		return route, "", nil
	}
	ips, err := catalog.ResolveProxyHost(ctx, route.ServerHost, route.AllowPrivate)
	if err != nil {
		return route, "", err
	}
	for _, ip := range ips {
		ip = ip.Unmap()
		if ip.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
			allowed := false
			for _, text := range route.ServerAllowCIDRs {
				if p, err := netip.ParsePrefix(text); err == nil && p.Contains(ip) {
					allowed = true
				}
			}
			if !allowed {
				return route, "", fmt.Errorf("proxy_private_CIDR_required")
			}
		}
	}
	var out map[string]any
	if json.Unmarshal(route.Outbound, &out) != nil {
		return route, "", fmt.Errorf("proxy_outbound_invalid")
	}
	if proxy, ok := out["proxySettings"].(map[string]any); ok && proxy["tag"] != nil && proxy["tag"] != "" {
		return route, "", fmt.Errorf("proxy_pin_detour_denied")
	}
	if stream, ok := out["streamSettings"].(map[string]any); ok {
		if sock, ok := stream["sockopt"].(map[string]any); ok && sock["dialerProxy"] != nil && sock["dialerProxy"] != "" {
			return route, "", fmt.Errorf("proxy_pin_detour_denied")
		}
	}
	settings, ok := out["settings"].(map[string]any)
	if !ok {
		return route, "", fmt.Errorf("proxy_server_schema")
	}
	key := "servers"
	switch out["protocol"] {
	case "vless", "vmess":
		key = "vnext"
	case "trojan", "shadowsocks", "socks", "http":
	default:
		return route, "", fmt.Errorf("proxy_server_unsupported")
	}
	servers, ok := settings[key].([]any)
	if !ok || len(servers) != 1 {
		return route, "", fmt.Errorf("proxy_server_schema")
	}
	server, ok := servers[0].(map[string]any)
	if !ok || server["address"] != route.ServerHost {
		return route, "", fmt.Errorf("proxy_server_identity_mismatch")
	}
	ip := ips[0].Unmap().String()
	if _, err := netip.ParseAddr(route.ServerHost); err != nil {
		if stream, ok := out["streamSettings"].(map[string]any); ok {
			security, _ := stream["security"].(string)
			if security == "tls" || security == "reality" {
				key := security + "Settings"
				tls, ok := stream[key].(map[string]any)
				if !ok {
					tls = map[string]any{}
					stream[key] = tls
				}
				if tls["serverName"] == nil || tls["serverName"] == "" {
					tls["serverName"] = route.ServerHost
				}
			}
			network, _ := stream["network"].(string)
			switch network {
			case "", "tcp", "raw":
			case "ws":
				ws, ok := stream["wsSettings"].(map[string]any)
				if !ok {
					ws = map[string]any{}
					stream["wsSettings"] = ws
				}
				headers, ok := ws["headers"].(map[string]any)
				if !ok {
					headers = map[string]any{}
					ws["headers"] = headers
				}
				if headers["Host"] == nil && headers["host"] == nil {
					headers["Host"] = route.ServerHost
				}
			case "grpc", "httpupgrade", "xhttp":
				key := network + "Settings"
				v, ok := stream[key].(map[string]any)
				if !ok {
					v = map[string]any{}
					stream[key] = v
				}
				field := "host"
				if network == "grpc" {
					field = "authority"
				}
				if v[field] == nil || v[field] == "" {
					v[field] = route.ServerHost
				}
			default:
				return route, "", fmt.Errorf("proxy_pin_transport_unsupported")
			}
		}
	}
	server["address"] = ip
	route.Outbound, _ = json.Marshal(out)
	return route, ip, nil
}

func (r *Runtime) refreshServerPins(ctx context.Context) {
	if !r.applyMu.TryLock() {
		return
	}
	defer r.applyMu.Unlock()
	r.mu.RLock()
	var old *engineEntry
	c := r.config
	for _, e := range r.current {
		if !e.route.Config.CheckServer || e.dialIP == "" {
			continue
		}
		observation := r.endpointIPs[endpointHost(e.route)]
		if observation.Observed.IsZero() || time.Since(observation.Observed) > 2*time.Minute {
			continue
		}
		found := false
		for _, ip := range observation.IPs {
			found = found || ip == e.dialIP
		}
		if !found {
			old = e
			break
		}
	}
	r.mu.RUnlock()
	if old == nil {
		return
	}
	next, err := r.ensure(ctx, c, old.route.Config)
	if err != nil || next == old {
		return
	}
	check := routeProbeConfig(c, old.route.Config.ID)
	if !next.route.Instance.CheckRoute(ctx, next.route.Config.Endpoint, check) {
		r.discardStaged(next)
		return
	}
	r.mu.Lock()
	if r.current[old.route.Config.ID] != old {
		r.mu.Unlock()
		r.discardStaged(next)
		return
	}
	next.staged = false
	r.current[old.route.Config.ID] = next
	r.installRolloversLocked(map[string]*engineEntry{old.route.Config.ID: old}, time.Now())
	r.rebuildLocked()
	r.mu.Unlock()
}
