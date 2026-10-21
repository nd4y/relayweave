package control

import "proxyplatform/internal/catalog"

// Provider metadata must not turn an existing allowed session into an implicit
// policy revocation. Hold a rotation which would leave an explicitly filtered
// pool; default automatic unfiltered pools never require this exception.
func rotationChangesMembership(source catalog.Source, id string, old, next catalog.Profile, pools []catalog.Pool) bool {
	if !old.Enabled || !old.Supported {
		return false
	}
	changed := func(f catalog.Filter) bool {
		matcher := f.Compile()
		before, _ := matcher.Match(id, old)
		after, _ := matcher.Match(id, next)
		return before && !after
	}
	if changed(source.Filter) {
		return true
	}
	contains := func(ids []string, value string) bool {
		for _, id := range ids {
			if id == value {
				return true
			}
		}
		return false
	}
	for _, pool := range pools {
		if !pool.Enabled {
			continue
		}
		wanted := pool.Automatic || contains(pool.SourceIDs, old.SourceID)
		if len(pool.ProfileIDs) > 0 {
			wanted = contains(pool.ProfileIDs, id)
		}
		if wanted && changed(pool.Filter) {
			return true
		}
	}
	return false
}
