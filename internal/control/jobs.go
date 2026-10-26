package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
)

type Operation struct {
	Action     string          `json:"action"`
	Target     string          `json:"target"`
	Status     string          `json:"status"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	Result     any             `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	Started    time.Time       `json:"started_at,omitempty"`
	Finished   time.Time       `json:"finished_at,omitempty"`
	LeaseUntil time.Time       `json:"lease_until,omitempty"`
}
type sourceSnapshot struct {
	HeldFingerprints []string         `json:"held_fingerprints,omitempty"`
	ReviewSnapshot   string           `json:"review_snapshot,omitempty"`
	SourceID         string           `json:"source_id"`
	Fetched          time.Time        `json:"fetched_at"`
	Body             []byte           `json:"body"`
	Parsed           catalog.Parsed   `json:"parsed"`
	ETag             string           `json:"etag"`
	LastModified     string           `json:"last_modified"`
	Quota            map[string]int64 `json:"quota"`
}

func (s *Server) enqueue(ctx context.Context, action, target string, payload json.RawMessage, key string) (store.Object, error) {
	id := ""
	if key != "" {
		if len(key) > 128 {
			return store.Object{}, errCode("idempotency_key_too_long")
		}
		sum := sha256.Sum256([]byte(action + "/" + target + "/" + key))
		id = hex.EncodeToString(sum[:16])
		if old, e := s.Store.Get(ctx, "operations", id); e == nil {
			prev, _ := catalog.Decode[Operation](old.Data)
			if string(prev.Payload) != string(payload) {
				return store.Object{}, errCode("idempotency_payload_conflict")
			}
			return old, nil
		}
	}
	v := Operation{Action: action, Target: target, Status: "queued", Payload: payload}
	b, _ := json.Marshal(v)
	o, e := s.Store.Put(ctx, store.Object{Kind: "operations", ID: id, Name: action + " " + target, Data: b}, 0)
	if errors.Is(e, store.ErrConflict) {
		return s.Store.Get(ctx, "operations", id)
	}
	if e == nil {
		select {
		case s.jobs <- struct{}{}:
		default:
		}
	}
	return o, e
}
func (s *Server) jobLoop() {
	sem := make(chan struct{}, 3)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.jobs:
		case <-ticker.C:
		}
		items, e := s.Store.Pending(s.ctx)
		if e != nil {
			continue
		}
		for _, o := range items {
			v, e := catalog.Decode[Operation](o.Data)
			if e != nil {
				continue
			}
			if v.Status != "queued" && !(v.Status == "running" && time.Now().After(v.LeaseUntil)) {
				continue
			}
			select {
			case sem <- struct{}{}:
			default:
				continue
			}
			v.Status = "running"
			v.Started = time.Now().UTC()
			v.LeaseUntil = time.Now().Add(5 * time.Minute)
			o.Data, _ = json.Marshal(v)
			claimed, e := s.Store.Put(s.ctx, o, o.Version)
			if e != nil {
				<-sem
				continue
			}
			go func(o store.Object, v Operation) { defer func() { <-sem }(); s.execute(o, v) }(claimed, v)
		}
	}
}
func (s *Server) execute(o store.Object, v Operation) {
	ctx, cancel := context.WithTimeout(s.ctx, 4*time.Minute)
	defer cancel()
	var result any
	var err error
	result, err = runOperation(func() (any, error) {
		switch v.Action {
		case "refresh", "test", "approve":
			result, err = s.refreshExpected(ctx, v.Target, v.Action, v.Payload)
		case "apply", "rollback", "reconcile":
			result, err = s.apply(ctx, v)
		case "install-core":
			result, err = s.installArtifact(ctx, v)
		case "approve-core":
			result, err = s.approveArtifact(ctx, v)
		case "test-profile", "speed-test", "drain", "restart-core", "core-cache", "remove-core":
			result, err = s.workerOperation(ctx, v, o.ID)
		default:
			err = errCode("unknown_operation")
		}
		return result, err
	})
	if err != nil {
		v.Status = "failed"
		v.Error = safeError(err)
	} else {
		v.Status = "succeeded"
		v.Result = result
	}
	v.Finished = time.Now().UTC()
	v.LeaseUntil = time.Time{}
	o.Data, _ = json.Marshal(v)
	if _, e := s.Store.Put(s.ctx, o, o.Version); e == nil {
		s.Store.Event(s.ctx, "operations", o.ID, v.Status, map[string]any{"action": v.Action, "target": v.Target, "error": v.Error})
		s.notify()
	}
}

var operationPanics atomic.Int64

func runOperation(action func() (any, error)) (result any, err error) {
	defer func() {
		if recover() != nil {
			operationPanics.Add(1)
			slog.Error("background operation failed unexpectedly; input and panic value suppressed")
			result = nil
			err = errCode("internal_operation_failure")
		}
	}()
	return action()
}
func safeError(e error) string {
	var known errCode
	if errors.As(e, &known) {
		return string(known)
	}
	message := e.Error()
	if len(message) < 80 && !strings.ContainsAny(message, " /?@:\n\r") {
		return message
	}
	return "operation_failed"
}
func (s *Server) refresh(ctx context.Context, id, action string) (any, error) {
	return s.refreshExpected(ctx, id, action, nil)
}
func (s *Server) refreshExpected(ctx context.Context, id, action string, expected json.RawMessage) (any, error) {
	// Serialize refreshes of a source using a DB-backed lock shared by API replicas.
	conn, e := s.Store.DB.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	defer conn.Release()
	lockID := int64(0)
	hash := sha256.Sum256([]byte(id))
	for _, b := range hash[:7] {
		lockID = (lockID << 8) | int64(b)
	}
	var locked bool
	if e = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&locked); e != nil || !locked {
		return nil, errCode("source_refresh_in_progress")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, `SELECT pg_advisory_unlock($1)`, lockID); err != nil {
			conn.Conn().Close(cleanup)
		}
	}()
	o, e := s.Store.Get(ctx, "sources", id)
	if e != nil {
		return nil, e
	}
	source, e := catalog.Decode[catalog.Source](o.Data)
	if e != nil {
		return nil, e
	}
	if action == "approve" && len(expected) > 0 {
		var body struct {
			Pending string `json:"pending_snapshot"`
			Version int64  `json:"source_version"`
		}
		if json.Unmarshal(expected, &body) != nil || body.Version != o.Version || body.Pending != source.PendingSnapshot {
			return nil, errCode("review_snapshot_changed")
		}
	}
	var snap sourceSnapshot
	if action == "approve" {
		if source.PendingSnapshot == "" {
			return nil, errCode("no_pending_snapshot")
		}
		pending, e := s.Store.Get(ctx, "snapshots", source.PendingSnapshot)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(pending.Data, &snap); e != nil {
			return nil, e
		}
	} else {
		fetchSource := sourceForRefresh(source, action)
		fetched, e := catalog.Fetch(ctx, fetchSource)
		if e != nil {
			if action != "test" {
				source.LastError = safeError(e)
				source.NextRefresh = time.Now().Add(5 * time.Minute)
				o.Data, _ = json.Marshal(source)
				s.Store.Put(ctx, o, o.Version)
			}
			return nil, e
		}
		if fetched.NotModified {
			source = sourceNotModified(source, fetched.Quota, time.Now().UTC())
			o.Data, _ = json.Marshal(source)
			if _, e = s.Store.Put(ctx, o, o.Version); e != nil {
				return nil, e
			}
			s.queueReconcile(ctx, id)
			return map[string]any{"not_modified": true}, nil
		}
		parsed, e := catalog.Parse(fetched.Body)
		if e != nil {
			if action != "test" {
				source.LastError = "subscription_parse_failed"
				source.NextRefresh = time.Now().Add(5 * time.Minute)
				o.Data, _ = json.Marshal(source)
				s.Store.Put(ctx, o, o.Version)
			}
			return nil, errCode("subscription_parse_failed")
		}
		snap = sourceSnapshot{SourceID: id, Fetched: time.Now().UTC(), Body: fetched.Body, Parsed: parsed, ETag: fetched.ETag, LastModified: fetched.LastModified, Quota: fetched.Quota}
	}
	for i := range snap.Parsed.Profiles {
		snap.Parsed.Profiles[i].SourceID = id
	}
	preview := profilePreview(snap.Parsed.Profiles, source.Filter)
	diff, e := s.sourceDiff(ctx, id, snap.Parsed.Profiles)
	if e != nil {
		return nil, e
	}
	preview["diff"] = diff
	if snap.Parsed.InvalidEntries > 0 {
		diff["requires_review"] = true
		diff["reason"] = "incomplete_subscription"
	}
	if source.UpdatePolicy == "review_only" && diff["address_rotations"].(int) > 0 {
		diff["requires_review"] = true
	}
	if !strings.HasPrefix(strings.ToLower(source.URL), "https://") && diff["address_rotations"].(int) > 0 {
		diff["requires_review"] = true
		diff["reason"] = "untrusted_source_transport"
	}
	if action == "test" {
		return preview, nil
	}
	if action != "approve" && snapshotReview(diff) {
		b, _ := json.Marshal(snap)
		pending, e := s.Store.Put(ctx, store.Object{Kind: "snapshots", Name: "pending " + id, Data: b}, 0)
		if e != nil {
			return nil, e
		}
		previousPending := source.PendingSnapshot
		source.PendingSnapshot = pending.ID
		source.LastError = "review_required"
		source.LastFetched = snap.Fetched
		if diff["filter_membership_changed"] == true {
			source.LastError = "endpoint_filter_membership_changed"
		}
		// Healthy independent changes continue while risky groups stay held.
		if source.UpdatePolicy != "review_only" && strings.HasPrefix(strings.ToLower(source.URL), "https://") {
			items, _, err := s.Store.List(ctx, "profiles", 20000, 0)
			if err != nil {
				return nil, err
			}
			previous := []catalog.Profile{}
			protected := map[string]bool{}
			heldIDs := map[string]bool{}
			if ids, ok := diff["filter_held_ids"].([]string); ok {
				for _, id := range ids {
					heldIDs[id] = true
				}
			}
			blockedOld := map[string]bool{}
			pools, _, err := s.Store.List(ctx, "pools", 100, 0)
			if err != nil {
				return nil, err
			}
			pins := map[string]bool{}
			for _, id := range source.Filter.ExcludedIDs {
				pins[id] = true
			}
			for _, poolObject := range pools {
				pool, _ := catalog.Decode[catalog.Pool](poolObject.Data)
				for _, id := range append(pool.ProfileIDs, pool.Filter.ExcludedIDs...) {
					pins[id] = true
				}
			}
			for _, item := range items {
				p, _ := catalog.Decode[catalog.Profile](item.Data)
				if p.SourceID == id {
					previous = append(previous, p)
					if heldIDs[item.ID] {
						blockedOld[p.Fingerprint] = true
					}
					if pins[item.ID] {
						protected[p.Fingerprint] = true
					}
				}
			}
			blockedNext := map[string]bool{}
			for nextFP, oldFP := range catalog.AddressMatchesProtected(previous, snap.Parsed.Profiles, protected) {
				if blockedOld[oldFP] {
					blockedNext[nextFP] = true
				}
			}
			merged, held, changed := safePartialHeld(previous, snap.Parsed.Profiles, protected, blockedNext)
			confirmed := false
			known := map[string]bool{}
			for _, p := range previous {
				if p.Present {
					known[p.Fingerprint] = true
				}
			}
			for _, p := range snap.Parsed.Profiles {
				if known[p.Fingerprint] && !blockedNext[p.Fingerprint] {
					confirmed = true
				}
			}
			if changed || confirmed {
				partial := snap
				partial.Parsed.Profiles = merged
				partial.HeldFingerprints = held
				partial.ReviewSnapshot = pending.ID
				if err = s.commitSnapshot(ctx, o, source, partial); err != nil {
					s.deleteSnapshot(ctx, pending.ID)
					return nil, err
				}
				if previousPending != "" {
					s.deleteSnapshot(ctx, previousPending)
				}
				s.queueReconcile(ctx, id)
				s.notify()
				return map[string]any{"partially_updated": true, "requires_review": true, "held": len(held), "diff": diff}, nil
			}
		}
		source.NextRefresh = time.Now().Add(time.Hour)
		o.Data, _ = json.Marshal(source)
		if _, e = s.Store.Put(ctx, o, o.Version); e != nil {
			s.Store.Delete(ctx, "snapshots", pending.ID, pending.Version)
			return nil, e
		}
		if previousPending != "" {
			s.deleteSnapshot(ctx, previousPending)
		}
		return map[string]any{"requires_review": true, "previous_count": source.ProfileCount, "new_count": len(snap.Parsed.Profiles), "preview": preview, "diff": diff}, nil
	}
	if e = s.commitSnapshot(ctx, o, source, snap); e != nil {
		if e.Error() == "rotation_waiting_drain" {
			source.LastError = "rotation_waiting_drain"
			source.LastFetched = snap.Fetched
			source.NextRefresh = time.Now().Add(time.Minute)
			o.Data, _ = json.Marshal(source)
			s.Store.Put(ctx, o, o.Version)
		}
		return nil, e
	}
	if bootstrap, err := s.ensureAutomaticDefaults(ctx); err != nil {
		return nil, err
	} else if bootstrap {
		if _, err = s.enqueue(ctx, "apply", "", nil, fmt.Sprintf("automatic-initial-%s-%d", id, o.Version)); err != nil {
			return nil, err
		}
	}
	s.queueReconcile(ctx, id)
	s.notify()
	return preview, nil
}
func (s *Server) queueReconcile(ctx context.Context, id string) {
	if err := s.publishLiveGuard(ctx, false); err != nil {
		slog.Error("live admission fence publication failed")
	}
	if d, e := s.Store.Get(ctx, "internal", "desired"); e == nil {
		key := time.Now().UTC().Format(time.RFC3339Nano)
		for _, kind := range []string{"sources", "profiles"} {
			if o, e := s.Store.Get(ctx, kind, id); e == nil {
				key = fmt.Sprintf("%s-%d", kind, o.Version)
				break
			}
		}
		s.enqueue(ctx, "reconcile", id, nil, "live-"+d.Name+"-"+key)
	}
}
func profilePreview(profiles []catalog.Profile, f catalog.Filter) map[string]any {
	rows := []map[string]any{}
	kept := 0
	matcher := f.Compile()
	for _, p := range profiles {
		ok, reason := matcher.Match("", p)
		if !p.Supported {
			ok = false
			reason = p.Reason
		}
		if ok {
			kept++
		}
		if len(rows) < 1000 {
			rows = append(rows, map[string]any{"name": p.Name, "host": p.Host, "port": p.Port, "protocol": p.Protocol, "transport": p.Transport, "country": p.Country, "udp": p.UDP, "included": ok, "reason": reason, "supported": p.Supported})
		}
	}
	return map[string]any{"total": len(profiles), "included": kept, "excluded": len(profiles) - kept, "items": rows, "truncated": len(profiles) > len(rows)}
}
func (s *Server) commitSnapshot(ctx context.Context, sourceObject store.Object, source catalog.Source, snap sourceSnapshot) error {
	release, lockErr := s.Store.AdvisoryLock(ctx, 723501992)
	if lockErr != nil {
		return lockErr
	}
	defer release()
	pendingID := source.PendingSnapshot
	previous, _, e := s.Store.List(ctx, "profiles", 20000, 0)
	if e != nil {
		return e
	}
	byIdentity := map[string][]store.Object{}
	byFingerprint := map[string]store.Object{}
	byEndpoint := map[string][]store.Object{}
	oldProfiles := []catalog.Profile{}
	oldSource := []store.Object{}
	for _, o := range previous {
		p, e := catalog.Decode[catalog.Profile](o.Data)
		if e != nil || p.SourceID != sourceObject.ID {
			continue
		}
		oldSource = append(oldSource, o)
		oldProfiles = append(oldProfiles, p)
		byIdentity[p.Identity] = append(byIdentity[p.Identity], o)
		byFingerprint[p.Fingerprint] = o
		byEndpoint[endpointIdentity(p)] = append(byEndpoint[endpointIdentity(p)], o)
	}
	frequency := map[string]int{}
	endpointFrequency := map[string]int{}
	counted := map[string]bool{}
	for _, p := range snap.Parsed.Profiles {
		if !counted[p.Fingerprint] {
			frequency[p.Identity]++
			endpointFrequency[endpointIdentity(p)]++
			counted[p.Fingerprint] = true
		}
	}
	protected := map[string]bool{}
	for _, id := range source.Filter.ExcludedIDs {
		protected[id] = true
	}
	pools, _, e := s.Store.List(ctx, "pools", 100, 0)
	if e != nil {
		return e
	}
	for _, o := range pools {
		p, _ := catalog.Decode[catalog.Pool](o.Data)
		for _, id := range append(p.Filter.ExcludedIDs, p.ProfileIDs...) {
			protected[id] = true
		}
	}
	protectedIdentity := map[string]bool{}
	protectedEndpoint := map[string]bool{}
	protectedFP := map[string]bool{}
	protectedTemplates := map[string]bool{}
	for _, o := range oldSource {
		prev, _ := catalog.Decode[catalog.Profile](o.Data)
		if !prev.Enabled || prev.CoreOverride || protected[o.ID] {
			protectedFP[prev.Fingerprint] = true
			if template := catalog.AddressTemplate(prev); template != "" {
				protectedTemplates[template] = true
			}
			protectedIdentity[prev.Identity] = true
			protectedEndpoint[endpointIdentity(prev)] = true
		}
	}
	rotations := catalog.AddressMatchesProtected(oldProfiles, snap.Parsed.Profiles, protectedFP)
	revisionRefs, canPrune, e := s.revisionReferences(ctx)
	if e != nil {
		return e
	}
	keep := map[string]bool{}
	unique := map[string]bool{}
	newObjects := []store.Object{}
	for _, p := range snap.Parsed.Profiles {
		if unique[p.Fingerprint] {
			continue
		}
		unique[p.Fingerprint] = true
		p.SourceID = sourceObject.ID
		p.Present = true
		p.Enabled = true
		p.CoreVersion = source.CoreVersion
		old, exists := byFingerprint[p.Fingerprint]
		if !exists && rotations[p.Fingerprint] != "" {
			old, exists = byFingerprint[rotations[p.Fingerprint]]
		}
		if !exists && len(byIdentity[p.Identity]) == 1 && frequency[p.Identity] == 1 {
			old = byIdentity[p.Identity][0]
			exists = true
		}
		if !exists && len(byEndpoint[endpointIdentity(p)]) == 1 && endpointFrequency[endpointIdentity(p)] == 1 {
			old = byEndpoint[endpointIdentity(p)][0]
			exists = true
		}
		id := store.NewID()
		version := int64(1)
		if exists {
			id = old.ID
			version = old.Version + 1
			prev, _ := catalog.Decode[catalog.Profile](old.Data)
			for _, held := range snap.HeldFingerprints {
				if held == p.Fingerprint {
					p.FrozenUntil = catalog.ProfileUntil(source, prev)
					break
				}
			}
			if prev.Fingerprint == p.Fingerprint {
				p.Previous = prev.Previous
			} else if rotations[p.Fingerprint] == prev.Fingerprint {
				for _, permit := range prev.Previous {
					if time.Now().Before(permit.ValidUntil) && (!canPrune || revisionRefs[permit.Fingerprint]) {
						p.Previous = append(p.Previous, permit)
					}
				}
				if len(p.Previous) >= 2 {
					return errCode("rotation_waiting_drain")
				}
				p.Previous = append(p.Previous, catalog.RevisionPermit{Fingerprint: prev.Fingerprint, ValidUntil: catalog.ProfileUntil(source, prev)})
			}
			p.Enabled = prev.Enabled
			if prev.CoreOverride {
				p.CoreVersion = prev.CoreVersion
				p.CoreOverride = true
			}
		} else if protectedIdentity[p.Identity] || protectedEndpoint[endpointIdentity(p)] || protectedTemplates[catalog.AddressTemplate(p)] {
			p.Enabled = false
			p.Warnings = append(p.Warnings, "ambiguous_identity_manual_override_held")
		}
		b, _ := json.Marshal(p)
		newObjects = append(newObjects, store.Object{Kind: "profiles", ID: id, Name: p.Name, Version: version, Data: b})
		keep[id] = true
	}
	for _, old := range oldSource {
		if !keep[old.ID] {
			p, e := catalog.Decode[catalog.Profile](old.Data)
			if e != nil {
				continue
			}
			if !p.Present {
				continue
			}
			p.Present = false
			p.RetiredUntil = catalog.ProfileUntil(source, p)
			old.Data, _ = json.Marshal(p)
			old.Version++
			newObjects = append(newObjects, old)
		}
	}
	currentElsewhere := 0
	for _, o := range previous {
		p, _ := catalog.Decode[catalog.Profile](o.Data)
		if p.SourceID != sourceObject.ID && p.Present {
			currentElsewhere++
		}
	}
	newCount := 0
	for _, o := range newObjects {
		if o.Version == 1 {
			newCount++
		}
	}
	if currentElsewhere+len(unique) > 10000 || len(previous)+newCount > 20000 {
		return store.ErrLimit
	}
	source.LastSuccess = snap.Fetched
	source.LastFetched = snap.Fetched
	source.NextRefresh = time.Now().Add(time.Duration(max(source.IntervalSeconds, 60)) * time.Second)
	source.LastError = ""
	source.PendingSnapshot = ""
	if snap.ReviewSnapshot != "" {
		source.PendingSnapshot = snap.ReviewSnapshot
		source.LastError = "partially_updated"
	}
	source.ETag = snap.ETag
	source.LastModified = snap.LastModified
	source.Quota = snap.Quota
	source.ProfileCount = len(unique)
	sourceObject.Data, _ = json.Marshal(source)
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	// CAS the source inside the same transaction as the derived catalog update.
	sealed, e := s.Store.Seal("sources", sourceObject.ID, sourceObject.Data)
	if e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE objects SET data=$1,version=version+1,updated_at=now() WHERE kind='sources' AND id=$2 AND version=$3`, sealed, sourceObject.ID, sourceObject.Version)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return store.ErrConflict
	}
	batch := &pgx.Batch{}
	for _, o := range newObjects {
		data, e := s.Store.Seal("profiles", o.ID, o.Data)
		if e != nil {
			return e
		}
		batch.Queue(`INSERT INTO objects(kind,id,name,version,data) VALUES('profiles',$1,$2,$3,$4) ON CONFLICT(kind,id) DO UPDATE SET name=excluded.name,version=objects.version+1,data=excluded.data,updated_at=now() WHERE objects.version=excluded.version-1`, o.ID, o.Name, o.Version, data)
	}
	results := tx.SendBatch(ctx, batch)
	for range newObjects {
		tag, err := results.Exec()
		if err != nil {
			results.Close()
			return err
		}
		if tag.RowsAffected() != 1 {
			results.Close()
			return store.ErrConflict
		}
	}
	if e = results.Close(); e != nil {
		return e
	}
	snapshotData, _ := json.Marshal(snap)
	snapID := store.NewID()
	sealed, e = s.Store.Seal("snapshots", snapID, snapshotData)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO objects(kind,id,name,data) VALUES('snapshots',$1,$2,$3)`, snapID, "snapshot "+sourceObject.ID, sealed); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM objects WHERE kind='snapshots' AND name=$1 AND id NOT IN (SELECT id FROM objects WHERE kind='snapshots' AND name=$1 ORDER BY updated_at DESC LIMIT 3)`, "snapshot "+sourceObject.ID); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	if pendingID != "" && pendingID != snap.ReviewSnapshot {
		s.deleteSnapshot(ctx, pendingID)
	}
	s.Store.Event(ctx, "sources", sourceObject.ID, "refreshed", map[string]any{"profiles": source.ProfileCount, "format": snap.Parsed.Format})
	return nil
}
func (s *Server) deleteSnapshot(ctx context.Context, id string) {
	if o, e := s.Store.Get(ctx, "snapshots", id); e == nil {
		s.Store.Delete(ctx, "snapshots", id, o.Version)
	}
}
func (s *Server) operations(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 1 && r.Method == "GET" {
		limit, offset := pageBounds(r)
		items, total, e := s.Store.List(r.Context(), "operations", limit, offset)
		if e != nil {
			fail(w, 503, "operations_unavailable")
			return
		}
		for i := range items {
			items[i] = s.redact(items[i])
		}
		respond(w, 200, map[string]any{"items": items, "total": total})
		return
	}
	if len(parts) == 2 && r.Method == "GET" {
		o, e := s.Store.Get(r.Context(), "operations", parts[1])
		if e != nil {
			fail(w, 404, "operation_not_found")
			return
		}
		respond(w, 200, s.redact(o))
		return
	}
	if len(parts) == 3 && parts[2] == "cancel" && r.Method == "POST" {
		o, e := s.Store.Get(r.Context(), "operations", parts[1])
		if e != nil {
			fail(w, 404, "operation_not_found")
			return
		}
		v, _ := catalog.Decode[Operation](o.Data)
		if v.Status != "queued" {
			fail(w, 409, "operation_already_started")
			return
		}
		v.Status = "cancelled"
		v.Finished = time.Now()
		o.Data, _ = json.Marshal(v)
		if _, e = s.Store.Put(r.Context(), o, o.Version); e != nil {
			fail(w, 409, "revision_conflict")
			return
		}
		respond(w, 200, s.redact(o))
		return
	}
	fail(w, 405, "method_not_allowed")
}
func (s *Server) preview(w http.ResponseWriter, r *http.Request, o store.Object) {
	if o.Kind == "sources" {
		p, _, e := s.Store.List(r.Context(), "profiles", 20000, 0)
		if e != nil {
			fail(w, 503, "catalog_unavailable")
			return
		}
		source, _ := catalog.Decode[catalog.Source](o.Data)
		if source.PendingSnapshot != "" {
			pending, e := s.Store.Get(r.Context(), "snapshots", source.PendingSnapshot)
			if e != nil {
				fail(w, 409, "pending_snapshot_unavailable_refresh_source")
				return
			}
			snap, e := catalog.Decode[sourceSnapshot](pending.Data)
			if e != nil {
				fail(w, 500, "snapshot_invalid")
				return
			}
			view := profilePreview(snap.Parsed.Profiles, source.Filter)
			view["requires_review"] = true
			view["diff"], _ = s.sourceDiff(r.Context(), o.ID, snap.Parsed.Profiles)
			respond(w, 200, view)
			return
		}
		profiles := []catalog.Profile{}
		for _, item := range p {
			v, _ := catalog.Decode[catalog.Profile](item.Data)
			if v.SourceID == o.ID && v.Present {
				profiles = append(profiles, v)
			}
		}
		respond(w, 200, profilePreview(profiles, source.Filter))
		return
	}
	if o.Kind == "pools" {
		items, e := s.poolProfiles(r.Context(), o.ID)
		if e != nil {
			fail(w, 400, safeError(e))
			return
		}
		limit, offset := pageBounds(r)
		total := len(items)
		items = items[min(offset, total):min(offset+limit, total)]
		rows := make([]store.Object, 0, len(items))
		for _, item := range items {
			rows = append(rows, s.redact(item))
		}
		respond(w, 200, map[string]any{"items": rows, "total": total, "limit": limit, "offset": offset})
		return
	}
	fail(w, 405, "preview_not_available")
}

var _ = fmt.Sprintf
