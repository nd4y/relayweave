package control

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net"
	"net/http"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/config"
	"proxyplatform/internal/store"
	"strings"
	"time"
)

func (s *Server) profileCommand(ctx context.Context, o store.Object) (json.RawMessage, error) {
	p, e := catalog.Decode[catalog.Profile](o.Data)
	if e != nil || !p.Supported {
		return nil, errCode("profile_not_supported")
	}
	settings, e := s.settings(ctx)
	if e != nil {
		return nil, e
	}
	version := settings.DefaultCore
	var validUntil time.Time
	if source, e := s.Store.Get(ctx, "sources", p.SourceID); e == nil {
		v, _ := catalog.Decode[catalog.Source](source.Data)
		if !v.Enabled || !p.Present || v.LastSuccess.IsZero() {
			return nil, errCode("profile_source_unavailable")
		}
		validUntil = v.LastSuccess.Add(time.Duration(v.MaxStaleSeconds) * time.Second)
		if expiry := v.Quota["expire"]; expiry > 0 && time.Unix(expiry, 0).Before(validUntil) {
			validUntil = time.Unix(expiry, 0)
		}
		if time.Now().After(validUntil) || (v.Quota["total"] > 0 && v.Quota["upload"]+v.Quota["download"] >= v.Quota["total"]) {
			return nil, errCode("profile_source_expired")
		}
		if v.CoreVersion != "" {
			version = v.CoreVersion
		}
	} else {
		return nil, errCode("profile_source_unavailable")
	}
	if p.CoreOverride && p.CoreVersion != "" {
		version = p.CoreVersion
	}
	if !coreSelector.MatchString(version) {
		return nil, errCode("invalid_core_version")
	}
	actual, _, _ := strings.Cut(version, "-")
	c := config.Core{Family: "xray", ID: "xray_probe", Version: actual, Artifact: version, Binary: "/cores/v" + version + "/xray"}
	if version == "26.10.17" {
		c.Binary = "/opt/xray/builtin/xray"
	}
	artifacts, e := s.approvedArtifacts(ctx)
	if e != nil {
		return nil, e
	}
	body := map[string]any{"route": config.Route{ID: o.ID, Core: c.ID, Endpoint: net.JoinHostPort("127.0.0.1", "45000"), Outbound: p.Outbound, UDP: p.UDP}, "core": c, "artifacts": artifacts, "probe_url": "https://www.gstatic.com/generate_204", "budget": 128 << 20}
	route := body["route"].(config.Route)
	route.ValidUntil = validUntil
	body["route"] = route
	policies, _, e := s.Store.List(ctx, "policies", 1000, 0)
	if e == nil {
		for _, po := range policies {
			policy, _ := catalog.Decode[catalog.Policy](po.Data)
			profiles, e := s.poolProfiles(ctx, policy.PoolID)
			if e != nil {
				continue
			}
			found := false
			for _, v := range profiles {
				if v.ID == o.ID {
					found = true
					break
				}
			}
			if found {
				body["policy_id"] = po.ID
				body["probe_url"] = policy.ProbeURL
				body["speed_url"] = policy.SpeedURL
				body["budget"] = policy.ProbeBudgetBytes
				break
			}
		}
	}
	return json.Marshal(body)
}
func (s *Server) trash(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 1 && r.Method == "GET" {
		items, _, e := s.Store.List(r.Context(), "trash", 1000, 0)
		if e != nil {
			fail(w, 503, "trash_unavailable")
			return
		}
		out := []map[string]any{}
		for _, o := range items {
			var b tombstone
			if json.Unmarshal(o.Data, &b) == nil && time.Since(b.Deleted) < 24*time.Hour {
				out = append(out, map[string]any{"id": o.ID, "name": o.Name, "kind": b.Original.Kind, "deleted_at": b.Deleted, "expires_at": b.Deleted.Add(24 * time.Hour)})
			}
		}
		respond(w, 200, map[string]any{"items": out})
		return
	}
	if len(parts) == 3 && parts[2] == "restore" && r.Method == "POST" {
		o, e := s.Store.Get(r.Context(), "trash", parts[1])
		if e != nil {
			fail(w, 404, "trash_not_found")
			return
		}
		var b tombstone
		if json.Unmarshal(o.Data, &b) != nil || time.Since(b.Deleted) > 24*time.Hour {
			fail(w, 410, "undo_expired")
			return
		}
		release, err := s.Store.AdvisoryLock(r.Context(), 723501992)
		if err != nil {
			fail(w, 503, "restore_unavailable")
			return
		}
		defer release()
		counts := map[string]int{b.Original.Kind: 1}
		for _, child := range b.Children {
			counts[child.Kind]++
		}
		for kind, adding := range counts {
			if budget := store.KindBudget(kind); budget > 0 {
				var count int
				if s.Store.DB.QueryRow(r.Context(), `SELECT count(*) FROM objects WHERE kind=$1`, kind).Scan(&count) != nil {
					fail(w, 503, "restore_unavailable")
					return
				}
				if count+adding > budget {
					fail(w, 409, "catalogue_budget_exceeded")
					return
				}
			}
		}
		if len(b.Children) > 0 {
			current, _, e := s.Store.List(r.Context(), "profiles", 20000, 0)
			if e != nil {
				fail(w, 503, "restore_unavailable")
				return
			}
			present := 0
			for _, p := range append(current, b.Children...) {
				v, _ := catalog.Decode[catalog.Profile](p.Data)
				if v.Present {
					present++
				}
			}
			if present > 10000 {
				fail(w, 409, "catalogue_budget_exceeded")
				return
			}
		}
		tx, e := s.Store.DB.Begin(r.Context())
		if e != nil {
			fail(w, 503, "restore_unavailable")
			return
		}
		defer tx.Rollback(r.Context())
		sealed, e := s.Store.Seal(b.Original.Kind, b.Original.ID, b.Original.Data)
		if e != nil {
			fail(w, 500, "restore_failed")
			return
		}
		tag, e := tx.Exec(r.Context(), `INSERT INTO objects(kind,id,name,version,data) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, b.Original.Kind, b.Original.ID, b.Original.Name, b.Original.Version+1, sealed)
		if e != nil || tag.RowsAffected() != 1 {
			fail(w, 409, "object_already_exists")
			return
		}
		batch := &pgx.Batch{}
		for _, child := range b.Children {
			data, e := s.Store.Seal(child.Kind, child.ID, child.Data)
			if e != nil {
				fail(w, 500, "restore_failed")
				return
			}
			batch.Queue(`INSERT INTO objects(kind,id,name,version,data) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, child.Kind, child.ID, child.Name, child.Version+1, data)
		}
		result := tx.SendBatch(r.Context(), batch)
		for range b.Children {
			tag, e := result.Exec()
			if e != nil || tag.RowsAffected() != 1 {
				result.Close()
				fail(w, 409, "child_profile_changed")
				return
			}
		}
		if result.Close() != nil {
			fail(w, 503, "restore_failed")
			return
		}
		tag, e = tx.Exec(r.Context(), `DELETE FROM objects WHERE kind='trash' AND id=$1 AND version=$2`, o.ID, o.Version)
		if e != nil || tag.RowsAffected() != 1 {
			fail(w, 409, "trash_changed")
			return
		}
		if tx.Commit(r.Context()) != nil {
			fail(w, 503, "restore_failed")
			return
		}
		s.Store.Event(r.Context(), b.Original.Kind, b.Original.ID, "restored", map[string]bool{"undo": true})
		s.notify()
		if b.Original.Kind == "sources" {
			s.queueReconcile(r.Context(), b.Original.ID)
		}
		respond(w, 200, map[string]bool{"restored": true})
		return
	}
	fail(w, 405, "method_not_allowed")
}

type tombstone struct {
	Children []store.Object `json:"children,omitempty"`
	Original store.Object   `json:"original"`
	Deleted  time.Time      `json:"deleted_at"`
}

func (s *Server) atomicDelete(ctx context.Context, o store.Object) error {
	release, err := s.Store.AdvisoryLock(ctx, 723501992)
	if err != nil {
		return err
	}
	defer release()
	children := []store.Object{}
	if o.Kind == "sources" {
		profiles, _, e := s.Store.List(ctx, "profiles", 20000, 0)
		if e != nil {
			return e
		}
		ids := map[string]bool{}
		for _, p := range profiles {
			v, _ := catalog.Decode[catalog.Profile](p.Data)
			if v.SourceID == o.ID {
				children = append(children, p)
				ids[p.ID] = true
			}
		}
		pools, _, e := s.Store.List(ctx, "pools", 10000, 0)
		if e != nil {
			return e
		}
		for _, p := range pools {
			v, _ := catalog.Decode[catalog.Pool](p.Data)
			for _, id := range append(v.ProfileIDs, v.Filter.ExcludedIDs...) {
				if ids[id] {
					return errCode("profile_referenced_by_pool")
				}
			}
		}
	}
	b, _ := json.Marshal(tombstone{Original: o, Children: children, Deleted: time.Now().UTC()})
	id := o.Kind + "-" + o.ID
	sealed, e := s.Store.Seal("trash", id, b)
	if e != nil {
		return e
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `DELETE FROM objects WHERE kind=$1 AND id=$2 AND version=$3`, o.Kind, o.ID, o.Version)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return store.ErrConflict
	}
	if len(children) > 0 {
		ids := []string{}
		versions := []int64{}
		for _, c := range children {
			ids = append(ids, c.ID)
			versions = append(versions, c.Version)
		}
		tag, e := tx.Exec(ctx, `DELETE FROM objects p USING unnest($1::text[],$2::bigint[]) AS d(id,version) WHERE p.kind='profiles' AND p.id=d.id AND p.version=d.version`, ids, versions)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != int64(len(children)) {
			return store.ErrConflict
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO objects(kind,id,name,data) VALUES('trash',$1,$2,$3) ON CONFLICT(kind,id) DO UPDATE SET name=excluded.name,data=excluded.data,version=objects.version+1,updated_at=now()`, id, o.Name, sealed); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	return s.Store.Event(ctx, o.Kind, o.ID, "deleted", map[string]int{"recoverable_hours": 24})
}

var _ = fmt.Sprint
