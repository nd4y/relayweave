package worker

import "time"

// Candidates sharing any endpoint address, transitively, share the first-stage
// allowance within an ISP. Include actual pinned sockets and retained DNS hints
// so expiry of a capacity observation cannot invent extra independent capacity.
// All inputs are bounded live engine/rollover snapshots; no network I/O occurs.
func (r *Runtime) firstCanaryPeersLocked(plan *pathRollover, now time.Time) []*pathRollover {
	routes := []*Route{}
	indices := map[*Route]int{}
	parent := []int{}
	add := func(v *Route) {
		if v == nil {
			return
		}
		if _, ok := indices[v]; !ok {
			indices[v] = len(routes)
			parent = append(parent, len(routes))
			routes = append(routes, v)
		}
	}
	for _, e := range r.entries {
		add(e.route)
	}
	for _, p := range r.rollovers {
		add(p.old)
		add(p.next)
	}
	add(plan.next)
	var root func(int) int
	root = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	owner := map[string]int{}
	join := func(v *Route, key string) {
		if key == "" {
			return
		}
		i := indices[v]
		key = v.Config.EgressID + "/" + key
		if old, ok := owner[key]; ok {
			parent[root(i)] = root(old)
		} else {
			owner[key] = i
		}
	}
	for _, v := range routes {
		host := endpointHost(v)
		if host != "" {
			join(v, "host:"+host)
		}
		for _, ip := range r.routeIPsLocked(v, now) {
			join(v, "ip:"+ip)
		}
		// DNS hints are used conservatively for caps even after their scorer TTL.
		// They authorize no dial and cannot revive an unhealthy/revoked endpoint.
		for _, ip := range r.endpointIPs[host].IPs {
			join(v, "ip:"+ip)
		}
	}
	for _, e := range r.entries {
		if e.dialIP != "" {
			join(e.route, "ip:"+e.dialIP)
		}
	}
	target := root(indices[plan.next])
	peers := []*pathRollover{}
	for _, p := range r.rollovers {
		if p.state == "canary" && p.step == 0 && root(indices[p.next]) == target {
			peers = append(peers, p)
		}
	}
	return peers
}

func (r *Runtime) firstCanaryAdmissionAllowedLocked(plan *pathRollover, now time.Time) bool {
	if plan.next.References.Load() >= 2 || (!plan.lastAdmission.IsZero() && now.Sub(plan.lastAdmission) < time.Second) {
		return false
	}
	peers := r.firstCanaryPeersLocked(plan, now)
	window := time.Time{}
	for _, p := range peers {
		if !p.windowAt.IsZero() && (window.IsZero() || p.windowAt.Before(window)) {
			window = p.windowAt
		}
	}
	reset := window.IsZero() || now.Sub(window) >= 30*time.Second
	if reset {
		window = now
	}
	var refs, used int64
	rateBlocked := false
	for _, p := range peers {
		bytes := p.next.BytesUp.Load() + p.next.BytesDown.Load()
		if reset || p.windowAt.IsZero() {
			p.windowBytes = bytes
		}
		p.windowAt = window
		refs += p.next.References.Load()
		used += max(int64(0), bytes-p.windowBytes)
		if !p.lastAdmission.IsZero() && now.Sub(p.lastAdmission) < time.Second {
			rateBlocked = true
		}
	}
	return !rateBlocked && refs < 2 && used < 8<<20
}
