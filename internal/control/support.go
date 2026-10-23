package control

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"proxyplatform/internal/store"
	"time"
)

func (s *Server) support(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		respond(w, 200, map[string]any{"files": []string{"redacted-settings.json", "catalog-metadata.json", "worker-status.json", "events.json", "control-metrics.txt"}, "excluded": []string{"subscription URLs and headers", "proxy passwords", "upstream outbounds and credentials", "private keys", "raw subscription bodies", "client addresses and destinations"}, "notice": "Names, server hostnames, versions and event times can contain personal information. Review before sharing."})
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "method_not_allowed")
		return
	}
	objects := map[string][]store.Object{}
	for _, kind := range []string{"sources", "aggregates", "pools", "profiles", "policies", "listeners", "settings"} {
		items, _, e := s.Store.List(r.Context(), kind, 10000, 0)
		if e != nil {
			fail(w, 503, "support_data_unavailable")
			return
		}
		for i := range items {
			items[i] = s.redact(items[i])
		}
		objects[kind] = items
	}
	events, _ := s.Store.Events(r.Context(), 0)
	rows, e := s.Store.DB.Query(r.Context(), `SELECT id,seen_at,generation,status FROM worker_status ORDER BY id`)
	if e != nil {
		fail(w, 503, "support_data_unavailable")
		return
	}
	workers := []map[string]any{}
	for rows.Next() {
		var id string
		var seen time.Time
		var gen int64
		var status json.RawMessage
		if rows.Scan(&id, &seen, &gen, &status) == nil {
			workers = append(workers, map[string]any{"id": id, "seen_at": seen, "generation": gen, "status": status})
		}
	}
	rows.Close()
	metrics := httptest.NewRecorder()
	s.Metrics(metrics, r)
	bundle := map[string]any{"schema": 1, "created_at": time.Now().UTC(), "configuration": objects, "workers": workers, "events": events, "control_metrics": metrics.Body.String()}
	var buffer bytes.Buffer
	zip := gzip.NewWriter(&buffer)
	if json.NewEncoder(zip).Encode(bundle) != nil || zip.Close() != nil {
		fail(w, 500, "support_encode_failed")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="relayweave-support-%s.json.gz"`, time.Now().UTC().Format("20060102T150405Z")))
	w.Write(buffer.Bytes())
}
