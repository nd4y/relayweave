package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"proxyplatform/internal/catalog"
	"proxyplatform/internal/config"
	"proxyplatform/internal/identity"
)

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	counts := map[string]int{}
	for _, kind := range []string{"sources", "aggregates", "pools", "profiles", "listeners", "policies"} {
		var n int
		if e := s.Store.DB.QueryRow(r.Context(), `SELECT count(*) FROM objects WHERE kind=$1`, kind).Scan(&n); e != nil {
			fail(w, 503, "overview_unavailable")
			return
		}
		counts[kind] = n
	}
	sources, _, e := s.Store.List(r.Context(), "sources", 1000, 0)
	if e != nil {
		fail(w, 503, "overview_unavailable")
		return
	}
	fresh, stale, errors := 0, 0, 0
	for _, o := range sources {
		v, _ := catalog.Decode[catalog.Source](o.Data)
		if v.LastSuccess.IsZero() || time.Now().After(v.LastSuccess.Add(time.Duration(v.MaxStaleSeconds)*time.Second)) {
			stale++
		} else {
			fresh++
		}
		if v.LastError != "" {
			errors++
		}
	}
	rows, e := s.Store.DB.Query(r.Context(), `SELECT id,seen_at,generation,status FROM worker_status ORDER BY id`)
	if e != nil {
		fail(w, 503, "overview_unavailable")
		return
	}
	defer rows.Close()
	workers := []map[string]any{}
	for rows.Next() {
		var id string
		var seen time.Time
		var generation int64
		var state json.RawMessage
		if rows.Scan(&id, &seen, &generation, &state) == nil {
			workers = append(workers, map[string]any{"id": id, "seen_at": seen, "generation": generation, "online": time.Since(seen) < 45*time.Second, "status": state})
		}
	}
	events, _ := s.Store.Events(r.Context(), 0)
	if len(events) > 10 {
		events = events[:10]
	}
	settings, _ := s.settings(r.Context())
	_, holdErr := s.Store.Get(r.Context(), "internal", "restore-pending")
	respond(w, 200, map[string]any{"counts": counts, "sources": map[string]int{"fresh": fresh, "stale": stale, "errors": errors}, "workers": workers, "events": events, "settings": settings, "restore_pending": holdErr == nil, "server_time": time.Now().UTC()})
}
func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, 405, "method_not_allowed")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "stream_unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	send := func() bool {
		if session, ok := r.Context().Value(sessionKey{}).(sessionData); ok && time.Now().After(session.Expires) {
			return false
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		events, e := s.Store.Events(ctx, after)
		if e != nil {
			return false
		}
		sort.Slice(events, func(i, j int) bool { return events[i].ID < events[j].ID })
		for _, event := range events {
			b, _ := json.Marshal(event)
			if _, e = fmt.Fprintf(w, "id: %d\nevent: change\ndata: %s\n\n", event.ID, b); e != nil {
				return false
			}
			after = event.ID
		}
		if len(events) == 0 {
			if _, e = fmt.Fprint(w, ": heartbeat\n\n"); e != nil {
				return false
			}
		}
		flusher.Flush()
		return true
	}
	if !send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}

// WorkerDiscovery retains known offline targets so unavailability remains alertable.
// HTTP SD retains its last successful list if this endpoint returns a DB error.
func (s *Server) WorkerDiscovery(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.Store.DB.Query(ctx, `SELECT id,status FROM worker_status ORDER BY id LIMIT 1001`)
	if err != nil {
		fail(w, 503, "discovery_unavailable")
		return
	}
	defer rows.Close()
	targets := []map[string]any{}
	for rows.Next() {
		var id string
		var raw []byte
		if rows.Scan(&id, &raw) != nil {
			fail(w, 503, "discovery_unavailable")
			return
		}
		var state struct {
			MetricsTarget string `json:"metrics_target"`
		}
		json.Unmarshal(raw, &state)
		address, err := config.MetricsTarget(id, state.MetricsTarget)
		if err != nil {
			address, err = config.MetricsTarget(id, "")
		}
		if err != nil {
			continue
		}
		targets = append(targets, map[string]any{"targets": []string{address}, "labels": map[string]string{"worker": id}})
		if len(targets) > 1000 {
			fail(w, 503, "discovery_limit")
			return
		}
	}
	if rows.Err() != nil {
		fail(w, 503, "discovery_unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 200, targets)
}

func (s *Server) Metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.Topology.Metrics(w)
	fmt.Fprintf(w, "relayweave_operation_panics_total %d\n", operationPanics.Load())
	for role, expiry := range identity.CertificateExpiries(s.IdentityDir) {
		fmt.Fprintf(w, "proxy_certificate_expiry_timestamp_seconds{role=%q} %d\n", role, expiry)
	}
	rows, e := s.Store.DB.Query(r.Context(), `SELECT kind,count(*) FROM objects WHERE kind IN ('sources','profiles','pools','listeners','policies') GROUP BY kind`)
	if e != nil {
		fmt.Fprintln(w, "relayweave_database_up 0")
		return
	}
	defer rows.Close()
	fmt.Fprintln(w, "relayweave_database_up 1")
	for rows.Next() {
		var kind string
		var n int
		rows.Scan(&kind, &n)
		fmt.Fprintf(w, "relayweave_objects{kind=%q} %d\n", kind, n)
	}
	sources, _, _ := s.Store.List(r.Context(), "sources", 1000, 0)
	for _, o := range sources {
		v, _ := catalog.Decode[catalog.Source](o.Data)
		ts := int64(0)
		if !v.LastSuccess.IsZero() {
			ts = v.LastSuccess.Unix()
		}
		fmt.Fprintf(w, "proxy_subscription_last_success_timestamp_seconds{source=%q} %d\n", o.ID, ts)
		enabled, failed := 0, 0
		if v.Enabled {
			enabled = 1
		}
		if v.LastError != "" {
			failed = 1
		}
		fmt.Fprintf(w, "proxy_subscription_enabled{source=%q} %d\nproxy_subscription_refresh_error{source=%q} %d\nproxy_subscription_max_stale_seconds{source=%q} %d\n", o.ID, enabled, o.ID, failed, o.ID, v.MaxStaleSeconds)
	}
	workers, e := s.Store.DB.Query(r.Context(), `SELECT id,extract(epoch from seen_at)::bigint,generation FROM worker_status`)
	if e == nil {
		defer workers.Close()
		for workers.Next() {
			var id string
			var ts, gen int64
			workers.Scan(&id, &ts, &gen)
			fmt.Fprintf(w, "relayweave_worker_last_seen_timestamp_seconds{worker=%q} %d\nrelayweave_worker_applied_generation{worker=%q} %d\n", id, ts, id, gen)
		}
	}
	if o, e := s.Store.Get(r.Context(), "internal", "desired"); e == nil {
		var d struct {
			Generation int64 `json:"generation"`
		}
		json.Unmarshal(o.Data, &d)
		fmt.Fprintf(w, "relayweave_desired_generation %d\n", d.Generation)
	}
}
func (s *Server) openapi(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, 405, "method_not_allowed")
		return
	}
	respond(w, 200, OpenAPI())
}
