package control

import (
	"encoding/json"
	"net/http"
)

func (s *Server) sourceRollovers(w http.ResponseWriter, r *http.Request, source string) {
	rows, err := s.Store.DB.Query(r.Context(), `SELECT id,seen_at,status FROM worker_status ORDER BY id LIMIT 100`)
	if err != nil {
		fail(w, 503, "rollover_status_unavailable")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var worker string
		var seen any
		var raw []byte
		if rows.Scan(&worker, &seen, &raw) != nil {
			fail(w, 503, "rollover_status_unavailable")
			return
		}
		var status struct {
			Rollovers []map[string]any `json:"rollovers"`
		}
		if json.Unmarshal(raw, &status) != nil {
			continue
		}
		for _, plan := range status.Rollovers {
			if plan["source_id"] == source {
				plan["worker_id"] = worker
				plan["seen_at"] = seen
				out = append(out, plan)
			}
		}
	}
	if rows.Err() != nil {
		fail(w, 503, "rollover_status_unavailable")
		return
	}
	respond(w, 200, map[string]any{"items": out, "source_id": source})
}
