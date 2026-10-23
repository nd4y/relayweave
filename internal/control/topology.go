package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Server) topologyAPI(w http.ResponseWriter, r *http.Request, parts []string) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "GET" {
		fail(w, 405, "read_only")
		return
	}
	if s.Topology == nil {
		fail(w, 503, "topology_unavailable")
		return
	}
	if len(r.URL.RawQuery) > 256 {
		fail(w, 400, "query_limit")
		return
	}
	if len(parts) < 2 {
		fail(w, 404, "not_found")
		return
	}
	switch parts[1] {
	case "snapshot":
		started := time.Now()
		b, err := json.Marshal(s.Topology.Snapshot())
		if err != nil {
			fail(w, 500, "snapshot_unavailable")
			return
		}
		s.Topology.RecordSnapshot(time.Since(started), len(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write(b)
	case "history":
		respond(w, 200, map[string]any{"items": s.Topology.History(), "step_seconds": 30, "retention_seconds": 3600})
	case "decisions":
		if len(parts) != 3 || len(parts[2]) > 96 {
			fail(w, 404, "expired_or_not_sampled")
			return
		}
		if d, ok := s.Topology.Decision(parts[2]); ok {
			respond(w, 200, d)
		} else {
			fail(w, 410, "expired_or_not_sampled")
		}
	case "events":
		s.topologyStream(w, r)
	default:
		fail(w, 404, "not_found")
	}
}
func (s *Server) topologyStream(w http.ResponseWriter, r *http.Request) {
	if !s.Topology.Subscribe() {
		fail(w, 429, "subscriber_limit")
		return
	}
	defer s.Topology.Unsubscribe()
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("cursor")
	}
	if len(cursor) > 96 || strings.ContainsAny(cursor, "\r\n") {
		fail(w, 400, "invalid_cursor")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "stream_unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(kind, id string, v any) bool {
		b, e := json.Marshal(v)
		if e != nil {
			return false
		}
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_, e = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, kind, b)
		flusher.Flush()
		return e == nil
	}
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		changed := s.Topology.Watch()
		events, valid := s.Topology.Replay(cursor)
		if !valid {
			if !send("resync", "", map[string]string{"reason": "cursor_gap_or_epoch_changed"}) {
				return
			}
			return
		}
		for _, e := range events {
			if !send("frame", e.Cursor, e) {
				return
			}
			cursor = e.Cursor
		}
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		case <-heartbeat.C:
			session, ok := r.Context().Value(sessionKey{}).(sessionData)
			if !ok || !time.Now().Before(session.Expires) {
				return
			}
			var active bool
			if err := s.Store.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sessions WHERE token_hash=$1 AND expires_at>now())`, session.Hash).Scan(&active); err != nil || !active {
				return
			}
			if !send("heartbeat", cursor, map[string]any{"server_time": time.Now().UTC()}) {
				return
			}
		}
	}
}
