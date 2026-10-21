package control

import (
	"context"
	"net"
	"proxyplatform/internal/catalog"
	"strconv"
	"strings"
)

func (s *Server) sourceDiff(ctx context.Context, id string, next []catalog.Profile) (map[string]any, error) {
	items, _, e := s.Store.List(ctx, "profiles", 20000, 0)
	if e != nil {
		return nil, e
	}
	previous := []catalog.Profile{}
	protectedIDs := map[string]bool{}
	filterPools := []catalog.Pool{}
	pools, _, e := s.Store.List(ctx, "pools", 100, 0)
	if e != nil {
		return nil, e
	}
	for _, o := range pools {
		p, _ := catalog.Decode[catalog.Pool](o.Data)
		if p.AggregateID != "" {
			aggregate, err := s.Store.Get(ctx, "aggregates", p.AggregateID)
			if err != nil {
				return nil, err
			}
			a, err := catalog.Decode[catalog.Aggregate](aggregate.Data)
			if err != nil {
				return nil, err
			}
			p.SourceIDs = append(p.SourceIDs, a.SourceIDs...)
		}
		filterPools = append(filterPools, p)
		for _, id := range append(p.Filter.ExcludedIDs, p.ProfileIDs...) {
			protectedIDs[id] = true
		}
	}
	sourceObject, e := s.Store.Get(ctx, "sources", id)
	if e != nil {
		return nil, e
	}
	source, _ := catalog.Decode[catalog.Source](sourceObject.Data)
	for _, id := range source.Filter.ExcludedIDs {
		protectedIDs[id] = true
	}
	protected := map[string]bool{}
	ids := map[string]string{}
	oldByFP := map[string]catalog.Profile{}
	for _, o := range items {
		p, _ := catalog.Decode[catalog.Profile](o.Data)
		if p.SourceID == id {
			previous = append(previous, p)
			ids[p.Fingerprint] = o.ID
			oldByFP[p.Fingerprint] = p
			if protectedIDs[o.ID] {
				protected[p.Fingerprint] = true
			}
		}
	}
	diff := compareProfilesProtected(previous, next, protected)
	matches := catalog.AddressMatchesProtected(previous, next, protected)
	heldIDs := []string{}
	for _, p := range next {
		if oldFP := matches[p.Fingerprint]; oldFP != "" && rotationChangesMembership(source, ids[oldFP], oldByFP[oldFP], p, filterPools) {
			diff["requires_review"] = true
			diff["filter_membership_changed"] = true
			diff["reason"] = "endpoint_filter_membership_changed"
			heldIDs = append(heldIDs, ids[oldFP])
		}
	}
	if len(heldIDs) > 0 {
		diff["filter_held_ids"] = heldIDs
	}
	return diff, nil
}
func compareProfiles(previous, next []catalog.Profile) map[string]any {
	return compareProfilesProtected(previous, next, nil)
}
func compareProfilesProtected(previous, next []catalog.Profile, protected map[string]bool) map[string]any {
	rotations := catalog.AddressMatchesProtected(previous, next, protected)
	blockedTemplates := map[string]bool{}
	for _, p := range previous {
		if p.Present && (!p.Enabled || p.CoreOverride || protected[p.Fingerprint]) {
			if template := catalog.AddressTemplate(p); template != "" {
				blockedTemplates[template] = true
			}
		}
	}
	rotatedOld := map[string]bool{}
	for _, fp := range rotations {
		rotatedOld[fp] = true
	}
	rotationItems := []map[string]any{}
	riskyCredentials := false
	oldFP, newFP := map[string]bool{}, map[string]bool{}
	oldID, newID := map[string]int{}, map[string]int{}
	oldEndpoint, newEndpoint := map[string]int{}, map[string]int{}
	unique := []catalog.Profile{}
	beforeSupported, afterSupported, beforeTotal := 0, 0, 0
	currentFP := map[string]bool{}
	for _, p := range previous {
		oldFP[p.Fingerprint] = true
		oldID[p.Identity]++
		oldEndpoint[endpointIdentity(p)]++
		if p.Present {
			beforeTotal++
			currentFP[p.Fingerprint] = true
			if p.Supported {
				beforeSupported++
			}
		}
	}
	for _, p := range next {
		if newFP[p.Fingerprint] {
			continue
		}
		newFP[p.Fingerprint] = true
		newID[p.Identity]++
		newEndpoint[endpointIdentity(p)]++
		unique = append(unique, p)
		if p.Supported {
			afterSupported++
		}
	}
	row := func(p catalog.Profile) map[string]any {
		return map[string]any{"name": p.Name, "protocol": p.Protocol, "host": p.Host, "port": p.Port, "country": p.Country, "supported": p.Supported, "reason": p.Reason}
	}
	added, removed, changed := []map[string]any{}, []map[string]any{}, []map[string]any{}
	retained, restored, ambiguous, parametersChanged := 0, 0, 0, 0
	for _, p := range unique {
		if oldFP[p.Fingerprint] {
			if currentFP[p.Fingerprint] {
				retained++
			} else {
				restored++
			}
			continue
		}
		if rotations[p.Fingerprint] != "" {
			rotationItems = append(rotationItems, row(p))
			continue
		}
		if oldID[p.Identity] == 1 && newID[p.Identity] == 1 {
			changed = append(changed, row(p))
			riskyCredentials = riskyCredentials || len(p.Outbound) > 0
		} else if oldEndpoint[endpointIdentity(p)] == 1 && newEndpoint[endpointIdentity(p)] == 1 {
			changed = append(changed, row(p))
			parametersChanged++
		} else {
			added = append(added, row(p))
			if oldID[p.Identity] > 0 || oldEndpoint[endpointIdentity(p)] > 0 || blockedTemplates[catalog.AddressTemplate(p)] {
				ambiguous++
			}
		}
	}
	for _, p := range previous {
		if !p.Present {
			continue
		}
		if newFP[p.Fingerprint] || rotatedOld[p.Fingerprint] || (oldID[p.Identity] == 1 && newID[p.Identity] == 1) {
			continue
		}
		if oldEndpoint[endpointIdentity(p)] == 1 && newEndpoint[endpointIdentity(p)] == 1 {
			continue
		}
		removed = append(removed, row(p))
	}
	return map[string]any{"address_rotations": len(rotationItems), "rotation_items": rotationItems[:min(200, len(rotationItems))], "added": len(added), "removed": len(removed), "credentials_changed": len(changed) - parametersChanged, "parameters_changed": parametersChanged, "retained": retained, "restored": restored, "ambiguous": ambiguous, "before_total": beforeTotal, "after_total": len(unique), "before_supported": beforeSupported, "after_supported": afterSupported, "added_items": added[:min(200, len(added))], "removed_items": removed[:min(200, len(removed))], "changed_items": changed[:min(200, len(changed))], "requires_review": riskyCredentials || ambiguous > 0 || parametersChanged > 0 || (beforeTotal > 0 && len(added)+len(removed) > (beforeTotal+len(unique))/2) || (beforeTotal > 0 && len(unique)*2 < beforeTotal) || (beforeSupported > 0 && afterSupported*2 < beforeSupported)}
}
func snapshotReview(diff map[string]any) bool { v, _ := diff["requires_review"].(bool); return v }

func endpointIdentity(p catalog.Profile) string {
	return net.JoinHostPort(strings.ToLower(p.Host), strconv.Itoa(p.Port))
}
