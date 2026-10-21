package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/config"
	"proxyplatform/internal/store"
)

func livePermits(now time.Time, sources []store.Object, profiles map[string]catalog.Profile) (map[string]config.RoutePermit, error) {
	type sourcePermit struct {
		until   time.Time
		matcher catalog.Matcher
	}
	allowed := map[string]sourcePermit{}
	for _, o := range sources {
		s, err := catalog.Decode[catalog.Source](o.Data)
		if err != nil {
			return nil, err
		}
		until := s.LastSuccess.Add(time.Duration(s.MaxStaleSeconds) * time.Second)
		if expiry := s.Quota["expire"]; expiry > 0 && time.Unix(expiry, 0).Before(until) {
			until = time.Unix(expiry, 0).UTC()
		}
		if !s.Enabled || s.LastSuccess.IsZero() || !now.Before(until) ||
			(s.Quota["total"] > 0 && s.Quota["upload"]+s.Quota["download"] >= s.Quota["total"]) {
			continue
		}
		allowed[o.ID] = sourcePermit{until: until, matcher: s.Filter.Compile()}
	}
	out := map[string]config.RoutePermit{}
	for id, p := range profiles {
		s, ok := allowed[p.SourceID]
		if !ok || !p.Present || !p.Enabled || !p.Supported {
			continue
		}
		if match, _ := s.matcher.Match(id, p); match {
			out[id] = config.RoutePermit{Fingerprint: p.Fingerprint, ValidUntil: s.until}
		}
	}
	return out, nil
}

func (s *Server) guardStamp(ctx context.Context) (string, error) {
	var stamp string
	err := s.Store.DB.QueryRow(ctx, `SELECT version::text || '/' ||
 COALESCE((SELECT jsonb_object_agg(id,version)::text FROM objects WHERE kind='sources'),'{}')
 FROM catalogue_epoch WHERE id=true`).Scan(&stamp)
	return stamp, err
}

// This lock is deliberately separate from the long-running rollout lock. A
// worker installing an artifact or a stalled Apply cannot delay revocations.
func (s *Server) publishLiveGuard(ctx context.Context, explicitApply bool) error {
	release, err := s.Store.AdvisoryLock(ctx, 723501993)
	if err != nil {
		return err
	}
	defer release()
	if !explicitApply {
		if _, err := s.Store.Get(ctx, "internal", "restore-pending"); err == nil {
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	var routes map[string]config.RoutePermit
	for {
		before, err := s.guardStamp(ctx)
		if err != nil {
			return err
		}
		profiles, err := s.profileCatalogue(ctx)
		if err != nil {
			return err
		}
		sources, _, err := s.Store.List(ctx, "sources", 50, 0)
		if err != nil {
			return err
		}
		routes, err = livePermits(time.Now(), sources, profiles.profiles)
		if err != nil {
			return err
		}
		after, err := s.guardStamp(ctx)
		if err != nil {
			return err
		}
		if before == after {
			break
		}
	}
	o, err := s.Store.Get(ctx, "internal", "live-guard")
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err == nil {
		old, err := catalog.Decode[config.LiveGuard](o.Data)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(old.Routes, routes) {
			return nil
		}
	}
	guard := config.LiveGuard{Version: o.Version + 1, Routes: routes}
	if err := guard.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(guard)
	_, err = s.Store.Put(ctx, store.Object{Kind: "internal", ID: "live-guard", Name: "Live admission fence", Data: b}, o.Version)
	return err
}

func (s *Server) liveGuard(ctx context.Context, acknowledged int64) (*config.LiveGuard, error) {
	var version int64
	err := s.Store.DB.QueryRow(ctx, `SELECT version FROM objects WHERE kind='internal' AND id='live-guard'`).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil || version <= acknowledged {
		return nil, err
	}
	s.guardMu.Lock()
	defer s.guardMu.Unlock()
	if s.guardCache != nil && s.guardCache.Version == version {
		return s.guardCache, nil
	}
	o, err := s.Store.Get(ctx, "internal", "live-guard")
	if err != nil {
		return nil, err
	}
	g, err := catalog.Decode[config.LiveGuard](o.Data)
	if err != nil || g.Version != o.Version || g.Validate() != nil {
		return nil, errCode("live_guard_corrupt")
	}
	s.guardCache = &g
	return &g, nil
}
