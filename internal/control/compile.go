package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"proxyplatform/internal/catalog"
	"proxyplatform/internal/config"
	"proxyplatform/internal/store"
)

type Revision struct {
	Generation int64                     `json:"generation"`
	Config     config.Config             `json:"config"`
	Status     string                    `json:"status"`
	Created    time.Time                 `json:"created_at"`
	Blueprint  map[string][]store.Object `json:"blueprint"`
	Previous   string                    `json:"previous,omitempty"`
	Warnings   []string                  `json:"warnings"`
}

func (s *Server) poolProfiles(ctx context.Context, id string) ([]store.Object, error) {
	return s.poolProfilesFrom(ctx, id, nil)
}
func (s *Server) poolProfilesFrom(ctx context.Context, id string, blueprint map[string][]store.Object) ([]store.Object, error) {
	return s.poolProfilesFromWithRetired(ctx, id, blueprint, false)
}
func (s *Server) poolProfilesFromWithRetired(ctx context.Context, id string, blueprint map[string][]store.Object, includeRetired bool) ([]store.Object, error) {
	get := func(kind, id string) (store.Object, error) {
		if blueprint != nil && (kind == "pools" || kind == "aggregates") {
			for _, o := range blueprint[kind] {
				if o.ID == id {
					return o, nil
				}
			}
			return store.Object{}, store.ErrNotFound
		}
		return s.Store.Get(ctx, kind, id)
	}
	po, e := get("pools", id)
	if e != nil {
		return nil, e
	}
	pool, e := catalog.Decode[catalog.Pool](po.Data)
	if e != nil {
		return nil, e
	}
	if !pool.Enabled {
		return []store.Object{}, nil
	}
	sourceIDs := append([]string(nil), pool.SourceIDs...)
	prefix, dedupe := false, true
	if pool.AggregateID != "" {
		o, e := get("aggregates", pool.AggregateID)
		if e != nil {
			return nil, errCode("aggregate_reference_missing")
		}
		a, e := catalog.Decode[catalog.Aggregate](o.Data)
		if e != nil {
			return nil, e
		}
		sourceIDs = append(a.SourceIDs, sourceIDs...)
		prefix, dedupe = a.PrefixNames, a.Dedupe
	}
	sources, _, e := s.Store.List(ctx, "sources", 1000, 0)
	if e != nil {
		return nil, e
	}
	sourceMap := map[string]catalog.Source{}
	sourceMatchers := map[string]catalog.Matcher{}
	poolMatcher := pool.Filter.Compile()
	sourceNames := map[string]string{}
	order := map[string]int{}
	for i, id := range sourceIDs {
		if _, exists := order[id]; !exists {
			order[id] = i + 1
		}
	}
	for _, o := range sources {
		if pool.Automatic {
			sourceIDs = append(sourceIDs, o.ID)
			order[o.ID] = len(order) + 1
		}
		v, e := catalog.Decode[catalog.Source](o.Data)
		if e == nil {
			sourceMap[o.ID] = v
			sourceMatchers[o.ID] = v.Filter.Compile()
			sourceNames[o.ID] = o.Name
		}
	}
	catalogue, e := s.profileCatalogue(ctx)
	if e != nil {
		return nil, e
	}
	items := append([]store.Object(nil), catalogue.items...)
	decoded := catalogue.profiles
	explicit := map[string]bool{}
	explicitOrder := map[string]int{}
	for i, id := range pool.ProfileIDs {
		explicit[id] = true
		if _, exists := explicitOrder[id]; !exists {
			explicitOrder[id] = i
		}
	}
	blocked := map[string]bool{}
	excluded := map[string]bool{}
	for _, id := range pool.Filter.ExcludedIDs {
		excluded[id] = true
	}
	for _, o := range items {
		if excluded[o.ID] {
			p := decoded[o.ID]
			blocked[p.Fingerprint] = true
		}
	}
	out := []store.Object{}
	seen := map[string]bool{}
	sort.SliceStable(items, func(i, j int) bool {
		if len(explicit) > 0 {
			return explicitOrder[items[i].ID] < explicitOrder[items[j].ID]
		}
		a := decoded[items[i].ID]
		b := decoded[items[j].ID]
		return order[a.SourceID] < order[b.SourceID]
	})
	for _, o := range items {
		p, exists := decoded[o.ID]
		if !exists || (!p.Present && (!includeRetired || !time.Now().Before(p.RetiredUntil))) || !p.Enabled || !p.Supported {
			continue
		}
		source, exists := sourceMap[p.SourceID]
		if !p.FrozenUntil.IsZero() && !time.Now().Before(p.FrozenUntil) {
			continue
		}
		if !exists || !source.Enabled || source.LastSuccess.IsZero() || time.Now().After(source.LastSuccess.Add(time.Duration(source.MaxStaleSeconds)*time.Second)) {
			continue
		}
		if _, wanted := order[p.SourceID]; !wanted && !explicit[o.ID] {
			continue
		}
		if len(explicit) > 0 && !explicit[o.ID] {
			continue
		}
		if ok, _ := sourceMatchers[p.SourceID].Match(o.ID, p); !ok {
			continue
		}
		if ok, _ := poolMatcher.Match(o.ID, p); !ok || blocked[p.Fingerprint] {
			continue
		}
		if source.Quota["total"] > 0 && source.Quota["upload"]+source.Quota["download"] >= source.Quota["total"] {
			continue
		}
		if expiry := source.Quota["expire"]; expiry > 0 && time.Now().Unix() >= expiry {
			continue
		}
		if dedupe && p.Present && seen[p.Fingerprint] {
			continue
		}
		if p.Present {
			seen[p.Fingerprint] = true
		}
		if prefix {
			o.Name = sourceNames[p.SourceID] + " / " + o.Name
		}
		out = append(out, o)
	}
	return out, nil
}
func (s *Server) compile(ctx context.Context, generation int64, blueprint map[string][]store.Object) (Revision, error) {
	if blueprint == nil {
		blueprint = map[string][]store.Object{}
		for _, kind := range []string{"aggregates", "pools", "policies", "listeners", "settings"} {
			items, _, e := s.Store.List(ctx, kind, 10000, 0)
			if e != nil {
				return Revision{}, e
			}
			blueprint[kind] = items
		}
	}
	get := func(kind, id string) (store.Object, error) {
		for _, o := range blueprint[kind] {
			if o.ID == id {
				return o, nil
			}
		}
		return store.Object{}, store.ErrNotFound
	}
	settingsObject, e := get("settings", "global")
	if e != nil {
		return Revision{}, e
	}
	settings, e := catalog.Decode[catalog.Settings](settingsObject.Data)
	if e != nil {
		return Revision{}, e
	}
	c := config.Config{StateDir: "/data/runtime/" + strconv.FormatInt(generation, 10), MetricsListen: config.MetricsAddress(), ConnectTimeoutMS: 3000, MaxConnections: 6000, LeaseUntil: time.Now().Add(time.Duration(settings.OfflineLeaseSeconds) * time.Second), UDP: config.UDP{Bind: "0.0.0.0", Advertise: settings.UDPAdvertise, PortMin: settings.UDPPortMin, PortMax: settings.UDPPortMax, IdleSeconds: 60}}
	c.Managed = true
	c.Generation = generation
	c.DownloadMbps = settings.DownloadMbps
	c.UploadMbps = settings.UploadMbps
	c.EgressPaths = settings.EgressPaths
	c.ProbeURL = "https://www.gstatic.com/generate_204"
	sources, _, e := s.Store.List(ctx, "sources", 1000, 0)
	if e != nil {
		return Revision{}, e
	}
	sourceMap := map[string]catalog.Source{}
	for _, o := range sources {
		v, _ := catalog.Decode[catalog.Source](o.Data)
		sourceMap[o.ID] = v
	}
	routeSeen := map[string]bool{}
	coreSeen := map[string]bool{}
	policySeen := map[string]bool{}
	poolCache := map[string][]store.Object{}
	listeners := map[string]bool{}
	warnings := []string{}
	for _, lo := range blueprint["listeners"] {
		listener, e := catalog.Decode[catalog.Listener](lo.Data)
		if e != nil {
			return Revision{}, e
		}
		if !listener.Enabled {
			continue
		}
		if listeners[listener.Listen] {
			return Revision{}, errCode("listener_port_conflict")
		}
		listeners[listener.Listen] = true
		po, e := get("policies", listener.PolicyID)
		if e != nil {
			return Revision{}, errCode("policy_reference_missing")
		}
		policy, e := catalog.Decode[catalog.Policy](po.Data)
		if e != nil {
			return Revision{}, e
		}
		if (policy.Mode == "auto" || policy.Mode == "throughput") && policy.SpeedURL == "" && !policy.DisableSpeedProbes {
			policy.SpeedURL = catalog.DefaultSpeedURL
		}
		if policy.DisableSpeedProbes {
			policy.SpeedURL = ""
		}
		profiles, cached := poolCache[policy.PoolID]
		if !cached {
			profiles, e = s.poolProfilesFromWithRetired(ctx, policy.PoolID, blueprint, true)
			if e != nil {
				return Revision{}, e
			}
			poolCache[policy.PoolID] = profiles
		}
		if len(profiles) == 0 {
			warnings = append(warnings, "no_eligible_profiles:"+po.ID)
		}
		cp := config.Policy{DisableSpeedProbes: policy.DisableSpeedProbes, ID: po.ID, Mode: policy.Mode, StickySeconds: policy.StickySeconds, RotationSeconds: policy.RotationSeconds, ProbeIntervalSeconds: policy.ProbeIntervalSeconds, ProbeURL: policy.ProbeURL, ProbeScope: policy.ProbeScope, SpeedURL: policy.SpeedURL, ProbeBudgetBytes: policy.ProbeBudgetBytes, ActivePaths: policy.ActivePaths, WarmReserve: policy.WarmReserve, MaxConnections: policy.MaxConnections, ConnectTimeoutMS: policy.ConnectTimeoutMS}
		cp.ProbeFallbackURLs = policy.ProbeFallbackURLs
		for _, profileObject := range profiles {
			p, _ := catalog.Decode[catalog.Profile](profileObject.Data)
			cp.Routes = append(cp.Routes, profileObject.ID)
			if routeSeen[profileObject.ID] {
				continue
			}
			source := sourceMap[p.SourceID]
			version := settings.DefaultCore
			if source.CoreVersion != "" {
				version = source.CoreVersion
			}
			if p.CoreOverride && p.CoreVersion != "" {
				version = p.CoreVersion
			}
			coreID := "xray_"
			if !coreSelector.MatchString(version) {
				return Revision{}, errCode("invalid_core_version")
			}
			for _, r := range version {
				if r == '.' || r == '-' {
					coreID += "_"
				} else if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' {
					coreID += string(r)
				} else {
					return Revision{}, errCode("invalid_core_version")
				}
			}
			if !coreSeen[coreID] {
				binary := "/cores/v" + version + "/xray"
				if version == "26.10.17" {
					binary = "/opt/xray/builtin/xray"
				}
				actual, _, _ := strings.Cut(version, "-")
				c.Cores = append(c.Cores, config.Core{Family: "xray", ID: coreID, Version: actual, Artifact: version, Binary: binary})
				coreSeen[coreID] = true
			}
			until := catalog.ProfileUntil(source, p)
			if !p.Present && p.RetiredUntil.Before(until) {
				until = p.RetiredUntil
			}
			if expiry := source.Quota["expire"]; expiry > 0 && time.Unix(expiry, 0).Before(until) {
				until = time.Unix(expiry, 0)
			}
			c.Routes = append(c.Routes, config.Route{ID: profileObject.ID, Core: coreID, Endpoint: net.JoinHostPort("127.0.0.1", strconv.Itoa(45000+len(c.Routes))), Outbound: p.Outbound, ValidUntil: until, CapacityGroup: p.SourceID + "/" + p.Host, ServerHost: p.Host, UDP: p.UDP})
			c.Routes[len(c.Routes)-1].Fingerprint = p.Fingerprint
			c.Routes[len(c.Routes)-1].EstablishedOnly = !p.Present
			c.Routes[len(c.Routes)-1].Previous = append([]config.RevisionPermit(nil), p.Previous...)
			c.Routes[len(c.Routes)-1].CheckServer = true
			c.Routes[len(c.Routes)-1].AllowPrivate = len(source.ProxyAllowCIDRs) > 0
			c.Routes[len(c.Routes)-1].ServerAllowCIDRs = append([]string(nil), source.ProxyAllowCIDRs...)
			routeSeen[profileObject.ID] = true
		}
		if !policySeen[po.ID] {
			c.Policies = append(c.Policies, cp)
			policySeen[po.ID] = true
		}
		if c.ProbeURL == "" {
			c.ProbeURL = policy.ProbeURL
		}
		c.Listeners = append(c.Listeners, config.Listener{ID: lo.ID, Protocol: listener.Protocol, Listen: listener.Listen, Policy: po.ID, Username: listener.Username, Password: listener.Password, UDP: listener.UDP, AllowCIDRs: listener.AllowCIDRs})
	}
	if len(c.Listeners) == 0 {
		warnings = append(warnings, "all_listeners_disabled")
	}
	totalHot := 0
	for _, p := range c.Policies {
		totalHot += p.ActivePaths + p.WarmReserve
	}
	if totalHot > 16 {
		return Revision{}, errCode("worker_hot_path_budget_exceeded")
	}
	approved, e := s.approvedArtifacts(ctx)
	if e != nil {
		return Revision{}, e
	}
	for _, c := range c.Cores {
		found := false
		for _, a := range approved {
			if a.ID() == c.Artifact {
				found = true
			}
		}
		if !found {
			return Revision{}, errCode("core_version_not_installed_or_approved")
		}
	}
	if e = c.ExpandEgress(); e != nil {
		return Revision{}, errCode("egress_config_invalid")
	}
	if e = c.Validate(); e != nil {
		return Revision{}, errCode("compiled_config_invalid")
	}
	return Revision{Generation: generation, Config: c, Status: "prepared", Created: time.Now().UTC(), Blueprint: blueprint, Warnings: warnings}, nil
}
func (s *Server) configuration(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 2 && parts[1] == "plan" && r.Method == "POST" {
		revision, e := s.compile(r.Context(), 0, nil)
		if e != nil {
			fail(w, 422, safeError(e))
			return
		}
		respond(w, 200, map[string]any{"valid": true, "routes": len(revision.Config.Routes), "cores": len(revision.Config.Cores), "listeners": len(revision.Config.Listeners), "warnings": revision.Warnings})
		return
	}
	if len(parts) == 2 && (parts[1] == "apply" || parts[1] == "rollback") && r.Method == "POST" {
		var body struct {
			RevisionID string `json:"revision_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		payload, _ := json.Marshal(body)
		op, e := s.enqueue(r.Context(), parts[1], body.RevisionID, payload, r.Header.Get("Idempotency-Key"))
		if e != nil {
			fail(w, 503, "operation_enqueue_failed")
			return
		}
		respond(w, 202, s.redact(op))
		return
	}
	if r.Method == "GET" {
		revisions, total, e := s.Store.List(r.Context(), "revisions", 100, 0)
		if e != nil {
			fail(w, 503, "revisions_unavailable")
			return
		}
		rows := []map[string]any{}
		for _, o := range revisions {
			v, _ := catalog.Decode[Revision](o.Data)
			rows = append(rows, map[string]any{"id": o.ID, "generation": v.Generation, "status": v.Status, "created_at": v.Created, "routes": len(v.Config.Routes), "listeners": len(v.Config.Listeners), "warnings": v.Warnings})
		}
		respond(w, 200, map[string]any{"items": rows, "total": total})
		return
	}
	fail(w, 405, "method_not_allowed")
}
func (s *Server) apply(ctx context.Context, op Operation) (any, error) {
	if op.Action == "reconcile" {
		if err := s.publishLiveGuard(ctx, false); err != nil {
			return nil, err
		}
	}
	release, e := s.Store.AdvisoryLock(ctx, 723501991)
	if e != nil {
		return nil, e
	}
	defer release()
	if op.Action == "reconcile" {
		if _, e := s.Store.Get(ctx, "internal", "restore-pending"); e == nil {
			return map[string]any{"skipped": "restored_draft_requires_apply"}, nil
		} else if !errors.Is(e, store.ErrNotFound) {
			return nil, e
		}
	}
	targets := map[string]bool{}
	rows, e := s.Store.DB.Query(ctx, `SELECT id FROM worker_status WHERE seen_at>now()-interval '60 seconds'`)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		targets[id] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(targets) == 0 {
		targets["worker-1"] = true
	}
	var generation int64
	if e := s.Store.DB.QueryRow(ctx, `SELECT nextval('config_generation')`).Scan(&generation); e != nil {
		return nil, e
	}
	var blueprint map[string][]store.Object
	if op.Action == "reconcile" {
		desired, e := s.Store.Get(ctx, "internal", "desired")
		if e != nil {
			return nil, errCode("no_active_configuration")
		}
		ro, e := s.Store.Get(ctx, "revisions", desired.Name)
		if e != nil {
			return nil, e
		}
		v, e := catalog.Decode[Revision](ro.Data)
		if e != nil {
			return nil, e
		}
		blueprint = v.Blueprint
	}
	if op.Action == "rollback" {
		o, e := s.Store.Get(ctx, "revisions", op.Target)
		if e != nil {
			return nil, errCode("rollback_revision_missing")
		}
		v, e := catalog.Decode[Revision](o.Data)
		if e != nil {
			return nil, e
		}
		blueprint = v.Blueprint
	}
	revision, e := s.compile(ctx, generation, blueprint)
	if e != nil {
		return nil, e
	}
	if e = s.publishLiveGuard(ctx, op.Action != "reconcile"); e != nil {
		return nil, e
	}
	current, e := s.Store.Get(ctx, "internal", "desired")
	expected := int64(0)
	if e == nil {
		expected = current.Version
		revision.Previous = current.Name
	} else if errors.Is(e, store.ErrNotFound) {
		// Get retains the requested kind/ID even when no row exists. An
		// absent pointer must not be mistaken for a previous revision.
		current = store.Object{}
	} else {
		return nil, e
	}
	b, _ := json.Marshal(revision)
	ro, e := s.Store.Put(ctx, store.Object{Kind: "revisions", Name: fmt.Sprintf("Revision %012d", generation), Data: b}, 0)
	if e != nil {
		return nil, e
	}
	pointer, _ := json.Marshal(map[string]any{"revision_id": ro.ID, "generation": generation})
	if _, e = s.Store.Put(ctx, store.Object{Kind: "internal", ID: "desired", Name: ro.ID, Data: pointer}, expected); e != nil {
		return nil, e
	}
	accepted := false
	defer func() {
		if !accepted {
			s.restoreDesired(current, ro)
		}
	}()
	s.notify()
	timer := time.NewTicker(500 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return map[string]any{"revision_id": ro.ID, "generation": generation}, errCode("rollout_timeout")
		case <-timer.C:
			allReady, failed := true, false
			for id := range targets {
				var applied int64
				var status []byte
				e = s.Store.DB.QueryRow(ctx, `SELECT generation,status FROM worker_status WHERE id=$1 AND seen_at>now()-interval '60 seconds'`, id).Scan(&applied, &status)
				if e != nil {
					allReady = false
					continue
				}
				var state map[string]any
				json.Unmarshal(status, &state)
				if applied != generation {
					allReady = false
				}
				if n, ok := state["failed_generation"].(float64); ok && int64(n) == generation {
					failed = true
				}
			}
			if allReady {
				accepted = true
				revision.Status = "active"
				ro.Data, _ = json.Marshal(revision)
				s.Store.Put(ctx, ro, ro.Version)
				s.Store.Event(ctx, "revisions", ro.ID, "active", map[string]any{"generation": generation})
				if op.Action != "reconcile" {
					if _, e := s.Store.DB.Exec(ctx, `DELETE FROM objects WHERE kind='internal' AND id='restore-pending'`); e != nil {
						return nil, e
					}
				}
				return map[string]any{"revision_id": ro.ID, "generation": generation, "status": "active"}, nil
			}
			if failed {
				revision.Status = "failed"
				ro.Data, _ = json.Marshal(revision)
				s.Store.Put(ctx, ro, ro.Version)
				return nil, errCode("worker_validation_failed")
			}
		}
	}
}
func (s *Server) restoreDesired(previous, failed store.Object) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	current, e := s.Store.Get(ctx, "internal", "desired")
	if e != nil || current.Name != failed.ID {
		return
	}
	desired, e := catalog.Decode[Revision](failed.Data)
	if e != nil {
		return
	}
	var prior *Revision
	if previous.ID != "" {
		old, err := s.Store.Get(ctx, "revisions", previous.Name)
		if err != nil {
			return
		}
		decoded, err := catalog.Decode[Revision](old.Data)
		if err != nil {
			return
		}
		prior = &decoded
	}
	revision := recoveryRevision(prior, desired)
	var generation int64
	if s.Store.DB.QueryRow(ctx, `SELECT nextval('config_generation')`).Scan(&generation) != nil {
		return
	}
	revision.Generation = generation
	revision.Status = "recovering"
	revision.Created = time.Now().UTC()
	revision.Previous = failed.ID
	b, _ := json.Marshal(revision)
	o, e := s.Store.Put(ctx, store.Object{Kind: "revisions", Name: fmt.Sprintf("Recovery %012d", generation), Data: b}, 0)
	if e != nil {
		return
	}
	pointer, _ := json.Marshal(map[string]any{"revision_id": o.ID, "generation": generation})
	s.Store.Put(ctx, store.Object{Kind: "internal", ID: "desired", Name: o.ID, Data: pointer}, current.Version)
	s.Store.Event(ctx, "revisions", failed.ID, "automatic_rollback", map[string]any{"recovery_generation": generation})
}

// A timed-out first rollout may still finish on a temporarily disconnected
// worker. An absent desired pointer sends it no new instruction. Publish a
// higher empty revision instead, and keep automatic routing empty until
// an explicit Apply establishes the first active blueprint.
func recoveryRevision(previous *Revision, desired Revision) Revision {
	if previous == nil || (len(previous.Config.Routes) == 0 && len(previous.Config.Listeners) == 0) {
		revision := Revision{Config: SafeRecovery(config.Config{}, desired.Config), Blueprint: map[string][]store.Object{"settings": append([]store.Object(nil), desired.Blueprint["settings"]...)}}
		revision.Config.Listeners = nil
		revision.Config.Policies = nil
		return revision
	}
	revision := *previous
	revision.Config = SafeRecovery(previous.Config, desired.Config)
	revision.Blueprint = recoveryBlueprint(desired.Blueprint, revision.Config)
	return revision
}

// Automatic source refreshes compile this blueprint, rather than the draft.
// Keep its health settings consistent with the recovered runtime, so an
// unrelated refresh cannot resurrect the rejected canary target.
func recoveryBlueprint(desired map[string][]store.Object, recovered config.Config) map[string][]store.Object {
	if desired == nil {
		return nil
	}
	out := make(map[string][]store.Object, len(desired))
	for kind, items := range desired {
		out[kind] = append([]store.Object(nil), items...)
	}
	probes := map[string]config.Policy{}
	for _, p := range recovered.Policies {
		probes[p.ID] = p
	}
	for i, o := range out["policies"] {
		p, ok := probes[o.ID]
		if !ok {
			continue
		}
		var data map[string]json.RawMessage
		if json.Unmarshal(o.Data, &data) != nil || data == nil {
			continue
		}
		for key, value := range map[string]any{"probe_url": p.ProbeURL, "probe_scope": p.ProbeScope, "probe_fallback_urls": p.ProbeFallbackURLs, "probe_interval_seconds": p.ProbeIntervalSeconds, "connect_timeout_ms": p.ConnectTimeoutMS} {
			data[key], _ = json.Marshal(value)
		}
		o.Data, _ = json.Marshal(data)
		out["policies"][i] = o
	}
	return out
}

// Rollback may restore a compatible previous core, but never removed paths,
// changed upstream credentials, revoked client passwords or widened ACLs.
func SafeRecovery(previous, desired config.Config) config.Config {
	desired.Policies = append([]config.Policy(nil), desired.Policies...)
	old := map[string]config.Route{}
	for _, v := range previous.Routes {
		old[v.ID] = v
	}
	available := map[string]bool{}
	kept := []config.Route{}
	for _, v := range desired.Routes {
		if prior, ok := old[v.ID]; ok && string(prior.Outbound) == string(v.Outbound) {
			prior.ValidUntil = v.ValidUntil
			prior.Fingerprint = v.Fingerprint
			kept = append(kept, prior)
			available[v.ID] = true
		}
	}
	desired.Routes = kept
	desired.Cores = previous.Cores
	if previous.ProbeURL != "" {
		desired.ProbeURL = previous.ProbeURL
		desired.ProbeFallbackURLs = append([]string(nil), previous.ProbeFallbackURLs...)
		desired.ProbeIntervalSeconds = previous.ProbeIntervalSeconds
	}
	oldPolicies := map[string]config.Policy{}
	for _, p := range previous.Policies {
		oldPolicies[p.ID] = p
	}
	for i, p := range desired.Policies {
		ids := []string{}
		for _, id := range p.Routes {
			if available[id] {
				ids = append(ids, id)
			}
		}
		desired.Policies[i].Routes = ids
		if prior, ok := oldPolicies[p.ID]; ok {
			desired.Policies[i].ProbeURL = prior.ProbeURL
			desired.Policies[i].ProbeFallbackURLs = append([]string(nil), prior.ProbeFallbackURLs...)
			desired.Policies[i].ProbeIntervalSeconds = prior.ProbeIntervalSeconds
			desired.Policies[i].ConnectTimeoutMS = prior.ConnectTimeoutMS
		}
	}
	return desired
}
