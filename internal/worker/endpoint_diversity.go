package worker

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// DNS is a capacity hint, never a change to the address that Xray connects to.
// Resolve outside the runtime lock, with at most four requests and a shared
// two-second deadline. A failed refresh retains a recent last-good observation.
type endpointResolution struct {
	IPs      []string
	Observed time.Time
	RetryAt  time.Time
}

func endpointHost(route *Route) string {
	host := route.Config.ServerHost
	if host == "" {
		// Compatibility with already persisted source/hostname assignments.
		_, host, _ = strings.Cut(route.Config.CapacityGroup, "/")
	}
	if host == "" {
		// A core warmed by a manual profile test may predate the assignment's
		// hostname metadata. Recover Xray's server address without mutating a live
		// Route.Config (gateway readers access that immutable struct concurrently).
		var outbound struct {
			Settings struct {
				Vnext []struct {
					Address string `json:"address"`
				} `json:"vnext"`
				Servers []struct {
					Address string `json:"address"`
				} `json:"servers"`
			} `json:"settings"`
		}
		if json.Unmarshal(route.Config.Outbound, &outbound) == nil {
			if len(outbound.Settings.Vnext) > 0 {
				host = outbound.Settings.Vnext[0].Address
			}
			if host == "" && len(outbound.Settings.Servers) > 0 {
				host = outbound.Settings.Servers[0].Address
			}
		}
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
}

func (r *Runtime) refreshEndpointIPs(ctx context.Context) {
	if !r.dnsMu.TryLock() {
		return
	}
	defer r.dnsMu.Unlock()
	now := time.Now()
	r.mu.Lock()
	if r.endpointIPs == nil {
		r.endpointIPs = map[string]endpointResolution{}
	}
	hosts := map[string]bool{}
	for _, e := range r.current {
		if host := endpointHost(e.route); host != "" {
			hosts[host] = true
		}
	}
	pending := []string{}
	for host := range r.endpointIPs {
		if !hosts[host] {
			delete(r.endpointIPs, host)
		}
	}
	for host := range hosts {
		if !now.Before(r.endpointIPs[host].RetryAt) {
			pending = append(pending, host)
		}
	}
	r.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	jobs := make(chan string, len(pending))
	for _, host := range pending {
		jobs <- host
	}
	close(jobs)
	var wg sync.WaitGroup
	for range min(4, len(pending)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for host := range jobs {
				var addresses []netip.Addr
				var err error
				if ip, e := netip.ParseAddr(host); e == nil {
					addresses = []netip.Addr{ip}
				} else {
					addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
				}
				seen := map[string]bool{}
				ips := []string{}
				for _, ip := range addresses {
					value := ip.Unmap().String()
					if !seen[value] {
						ips = append(ips, value)
						seen[value] = true
					}
				}
				sort.Strings(ips)
				r.mu.Lock()
				old := r.endpointIPs[host]
				if err == nil && len(ips) > 0 {
					r.endpointIPs[host] = endpointResolution{IPs: ips, Observed: now, RetryAt: now.Add(time.Minute)}
				} else {
					old.RetryAt = now.Add(10 * time.Second)
					r.endpointIPs[host] = old
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

func (r *Runtime) routeIPsLocked(route *Route, now time.Time) []string {
	host := endpointHost(route)
	if ip, err := netip.ParseAddr(host); err == nil {
		return []string{ip.Unmap().String()}
	}
	value := r.endpointIPs[host]
	if value.Observed.IsZero() || now.Sub(value.Observed) > 5*time.Minute {
		return nil
	}
	return value.IPs
}

// Shared A/AAAA addresses join profiles even across subscriptions, names and
// ports. Overlapping DNS sets are conservatively one balancer, transitively.
// Keep duplicates after independent candidates: they remain usable for failover.
func (r *Runtime) endpointGroupsLocked(routes []*Route, now time.Time) []string {
	parent := make([]int, len(routes))
	for i := range parent {
		parent[i] = i
	}
	var root func(int) int
	root = func(i int) int {
		if parent[i] != i {
			parent[i] = root(parent[i])
		}
		return parent[i]
	}
	owner := map[string]int{}
	for i, route := range routes {
		keys := []string{}
		for _, ip := range r.routeIPsLocked(route, now) {
			keys = append(keys, "ip:"+ip)
		}
		if len(keys) == 0 {
			key := endpointHost(route)
			if key == "" {
				key = route.Config.CapacityGroup
			}
			if key == "" {
				key = route.Config.ID
			}
			keys = []string{"host:" + key}
		}
		for _, key := range keys {
			if j, ok := owner[key]; ok {
				parent[root(i)] = root(j)
			} else {
				owner[key] = i
			}
		}
	}
	groups := make([]string, len(routes))
	for i := range routes {
		groups[i] = routes[root(i)].Config.ID
	}
	return groups
}
