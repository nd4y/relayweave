package worker

import "proxyplatform/internal/topology"

// Catalogue IDs survive a rotation; engine identity must describe the actual
// old/new process that received this admission, not the latest ID mapping.
func (g *Gateway) topologyRouteResult(d *topology.Decision, route *Route, reason string) {
	if d != nil && g.Dynamic != nil {
		_, engine := g.Dynamic.topologyIdentity(route)
		for i := range d.Candidates {
			if d.Candidates[i].Route == route.Config.ID {
				d.Candidates[i].Engine = engine
			}
		}
	}
	g.topologyResult(d, route.Config.ID, reason)
}
