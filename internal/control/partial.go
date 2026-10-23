package control

import "proxyplatform/internal/catalog"

// Retain every old record when completeness/trust requires a hold. Only exact
// records, proven rotations and genuinely new safe records may make progress.
func safePartial(previous, next []catalog.Profile, protected map[string]bool) ([]catalog.Profile, []string, bool) {
	return safePartialHeld(previous, next, protected, nil)
}
func safePartialHeld(previous, next []catalog.Profile, protected, blockedNext map[string]bool) ([]catalog.Profile, []string, bool) {
	matches := catalog.AddressMatchesProtected(previous, next, protected)
	byFP := map[string]catalog.Profile{}
	blockedTemplates := map[string]bool{}
	identities, endpoints := map[string]bool{}, map[string]bool{}
	for _, p := range previous {
		if p.Present {
			if !p.Enabled || p.CoreOverride || protected[p.Fingerprint] {
				if template := catalog.AddressTemplate(p); template != "" {
					blockedTemplates[template] = true
				}
			}
			byFP[p.Fingerprint] = p
			identities[p.Identity] = true
			endpoints[endpointIdentity(p)] = true
		}
	}
	result := map[string]catalog.Profile{}
	held := map[string]bool{}
	for fp, p := range byFP {
		result[fp] = p
		held[fp] = true
	}
	changed := false
	for _, p := range next {
		if blockedNext[p.Fingerprint] {
			continue
		}
		if _, ok := byFP[p.Fingerprint]; ok {
			result[p.Fingerprint] = p
			delete(held, p.Fingerprint)
			continue
		}
		if old := matches[p.Fingerprint]; old != "" {
			delete(result, old)
			delete(held, old)
			result[p.Fingerprint] = p
			changed = true
			continue
		}
		template := catalog.AddressTemplate(p)
		if !identities[p.Identity] && !endpoints[endpointIdentity(p)] && template != "" && !blockedTemplates[template] {
			result[p.Fingerprint] = p
			changed = true
		}
	}
	out := []catalog.Profile{}
	frozen := []string{}
	for fp, p := range result {
		out = append(out, p)
		if held[fp] {
			frozen = append(frozen, fp)
		}
	}
	return out, frozen, changed
}
