package worker

// Called only with collection enabled. Entries are capped at processLimit;
// retiring instances are identified by pointer, never by a reused profile ID.
func (r *Runtime) topologyIdentity(route *Route) (int64, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, entry := range r.entries {
		if entry.route == route {
			return r.config.Generation, entry.key
		}
	}
	return r.config.Generation, ""
}
