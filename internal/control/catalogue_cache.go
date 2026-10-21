package control

import (
	"context"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
)

type decodedCatalogue struct {
	epoch    int64
	items    []store.Object
	profiles map[string]catalog.Profile
}

// All returned values are immutable. Callers sort a copy, never this slice.
func (s *Server) profileCatalogue(ctx context.Context) (*decodedCatalogue, error) {
	s.catalogueMu.Lock()
	defer s.catalogueMu.Unlock()
	for {
		var epoch int64
		if err := s.Store.DB.QueryRow(ctx, `SELECT version FROM catalogue_epoch WHERE id=true`).Scan(&epoch); err != nil {
			return nil, err
		}
		if s.catalogue != nil && s.catalogue.epoch == epoch {
			return s.catalogue, nil
		}
		items, _, err := s.Store.List(ctx, "profiles", 20000, 0)
		if err != nil {
			return nil, err
		}
		result := &decodedCatalogue{epoch: epoch, items: items, profiles: make(map[string]catalog.Profile, len(items))}
		for _, item := range items {
			p, err := catalog.Decode[catalog.Profile](item.Data)
			if err != nil {
				return nil, err
			}
			result.profiles[item.ID] = p
		}
		var current int64
		if err := s.Store.DB.QueryRow(ctx, `SELECT version FROM catalogue_epoch WHERE id=true`).Scan(&current); err != nil {
			return nil, err
		}
		if current != epoch {
			continue
		}
		s.catalogue = result
		return result, nil
	}
}
