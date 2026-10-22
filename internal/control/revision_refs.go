package control

import (
	"context"
	"encoding/json"
	"time"
)

// Absence is evidence only when every known worker has recently acknowledged
// the desired generation and exports revision-aware status. Offline workers
// conservatively pin old grants until their frozen expiry.
func (s *Server) revisionReferences(ctx context.Context) (map[string]bool, bool, error) {
	refs := map[string]bool{}
	desired, err := s.Store.Get(ctx, "internal", "desired")
	if err != nil {
		return refs, false, nil
	}
	var pointer struct {
		Generation int64 `json:"generation"`
	}
	if json.Unmarshal(desired.Data, &pointer) != nil {
		return refs, false, nil
	}
	rows, err := s.Store.DB.Query(ctx, `SELECT generation,seen_at,status FROM worker_status`)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	all, count := true, 0
	for rows.Next() {
		var generation int64
		var seen time.Time
		var raw []byte
		if err = rows.Scan(&generation, &seen, &raw); err != nil {
			return nil, false, err
		}
		var status struct {
			RoutingVersion int `json:"routing_version"`
			Cores          []struct {
				Revision string `json:"revision"`
			} `json:"cores"`
		}
		if json.Unmarshal(raw, &status) != nil {
			all = false
			continue
		}
		count++
		if generation < pointer.Generation || time.Since(seen) > 60*time.Second || status.RoutingVersion < 3 {
			all = false
		}
		for _, core := range status.Cores {
			if core.Revision != "" {
				refs[core.Revision] = true
			}
		}
	}
	return refs, all && count > 0, rows.Err()
}
