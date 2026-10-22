package worker

import "strings"

func (r *Runtime) waitingSourcesLocked() []string {
	waiting := map[string]bool{}
	for _, p := range r.policies {
		if len(p.best) > 0 {
			continue
		}
		for _, route := range r.config.Routes {
			if _, ok := p.rank[route.ID]; ok && !route.EstablishedOnly {
				source, _, _ := strings.Cut(route.CapacityGroup, "/")
				if source != "" {
					waiting[source] = true
				}
			}
		}
	}
	out := []string{}
	for source := range waiting {
		out = append(out, source)
	}
	return out
}
