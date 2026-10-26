package topology

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

const MaxWorkers = 16
const MaxRoutes = 128
const ReplayLimit = 32
const DecisionLimit = 10000

type Event struct {
	Cursor string `json:"cursor"`
	Worker string `json:"worker"`
	Frame  Frame  `json:"frame"`
}
type Summary struct {
	At               time.Time `json:"at"`
	Worker           string    `json:"worker"`
	Active, Up, Down int64
	Generation       int64 `json:"generation"`
}
type Hub struct {
	mu            sync.Mutex
	changed       chan struct{}
	epoch         string
	seq           uint64
	workers       map[string]Frame
	events        []Event
	decisions     map[string]Decision
	order         []string
	history       []Summary
	lastHistory   map[string]time.Time
	subscribers   int
	decisionLimit int
	decisionTTL   time.Duration
	snapshotNanos int64
	snapshotBytes int
	deliveryDelay float64
	subscriberLag uint64
}

func NewHub() *Hub {
	o := NewObserver()
	epoch := fmt.Sprintf("%x", time.Now().UnixNano())
	if o != nil {
		epoch = o.Epoch
	}
	return &Hub{changed: make(chan struct{}), epoch: epoch, workers: map[string]Frame{}, decisions: map[string]Decision{}, lastHistory: map[string]time.Time{}, decisionLimit: boundedEnv("TOPOLOGY_DECISION_LIMIT", DecisionLimit, 32, DecisionLimit), decisionTTL: time.Duration(boundedEnv("TOPOLOGY_DETAIL_SECONDS", 900, 30, 900)) * time.Second}
}

// Watch is captured before Replay so acceptance between replay and waiting
// cannot be lost. A generation channel wakes all subscribers without blocking.
func (h *Hub) Watch() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.changed
}
func (h *Hub) cursor() string { return fmt.Sprintf("%s:%d", h.epoch, h.seq) }
func (h *Hub) Accept(worker string, f Frame) bool {
	f.Changes = nil // Never accept a worker-supplied control-plane interpretation.
	if !validFrame(f) {
		return false
	}
	if len(f.Routes) > MaxRoutes || len(f.Listeners) > 64 || len(f.Decisions) > 256 || len(f.Epoch) > 32 {
		return false
	}
	for _, d := range f.Decisions {
		if len(d.Candidates) > 32 || len(d.ID) > 96 {
			return false
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	old, exists := h.workers[worker]
	if !exists && len(h.workers) >= MaxWorkers {
		return false
	}
	if exists && old.Epoch == f.Epoch && f.Sequence <= old.Sequence {
		return false
	}
	f.Worker = worker
	f.Changes = h.changes(old, f, exists)
	for i := range f.Decisions {
		f.Decisions[i].Worker = worker
		f.Decisions[i].Epoch = f.Epoch
	}
	now := time.Now()
	h.expireLocked(now)
	fresh := make([]Decision, 0, len(f.Decisions))
	for _, d := range f.Decisions {
		if now.Sub(d.At) <= h.decisionTTL && !d.At.After(now.Add(time.Minute)) {
			fresh = append(fresh, d)
		}
	}
	f.Decisions = fresh
	h.deliveryDelay = max(0, now.Sub(f.At).Seconds())
	for _, d := range f.Decisions {
		if now.Sub(d.At) > h.decisionTTL || d.At.After(now.Add(time.Minute)) {
			continue
		}
		if _, ok := h.decisions[d.ID]; ok {
			continue
		}
		h.decisions[d.ID] = d
		h.order = append(h.order, d.ID)
	}
	for len(h.order) > 0 {
		d := h.decisions[h.order[0]]
		if len(h.order) <= h.decisionLimit && now.Sub(d.At) <= h.decisionTTL {
			break
		}
		delete(h.decisions, h.order[0])
		h.order = h.order[1:]
	}
	// Keep the recent timeline across idle frames, within the same worker epoch.
	if exists && old.Epoch == f.Epoch {
		previous := []Decision{}
		seen := map[string]bool{}
		for _, d := range f.Decisions {
			seen[d.ID] = true
		}
		for _, d := range old.Decisions {
			if !seen[d.ID] && now.Sub(d.At) <= h.decisionTTL {
				previous = append(previous, d)
			}
		}
		f.Decisions = append(previous, f.Decisions...)
	}
	// Frames retain only a small recent event list. Detail retention is independent.
	if len(f.Decisions) > 32 {
		f.Decisions = append([]Decision(nil), f.Decisions[len(f.Decisions)-32:]...)
	}
	h.workers[worker] = f
	h.seq++
	close(h.changed)
	h.changed = make(chan struct{})
	h.events = append(h.events, Event{Cursor: h.cursor(), Worker: worker, Frame: f})
	if len(h.events) > ReplayLimit {
		h.events = append([]Event(nil), h.events[len(h.events)-ReplayLimit:]...)
	}
	if now.Sub(h.lastHistory[worker]) >= 30*time.Second {
		v := Summary{At: f.At, Worker: worker, Generation: f.Generation}
		for _, r := range f.Routes {
			v.Active += r.Active
			v.Up += r.Up
			v.Down += r.Down
		}
		h.history = append(h.history, v)
		h.lastHistory[worker] = now
	}
	for len(h.history) > 0 && (now.Sub(h.history[0].At) > time.Hour || len(h.history) > 1920) {
		h.history = h.history[1:]
	}
	return true
}
func (h *Hub) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expireLocked(time.Now())
	s := Snapshot{Schema: 1, ServerTime: time.Now().UTC(), Cursor: h.cursor(), Workers: []Frame{}, Completeness: "partial", WindowSeconds: 1, Limitations: []string{"HTTPS URL/status and application identity unknown without explicit integration", "Server DNS IP is not a measured public exit IP; shared IP is a capacity hint", "Listener links are configured; route counters aggregate TCP and UDP; client/destination groups are intentionally unknown", "Decisions use worker-configured sampling; omitted events do not imply no traffic; no TCP migration is inferred"}, Retention: map[string]int{"decision_limit": DecisionLimit, "decision_seconds": 900, "aggregate_seconds": 3600, "aggregate_step_seconds": 30, "replay_frames": ReplayLimit, "max_workers": MaxWorkers, "max_routes_per_worker": MaxRoutes}}
	s.Retention["decision_limit"] = h.decisionLimit
	s.Retention["max_changes_per_worker"] = ChangeLimit
	s.Retention["decision_seconds"] = int(h.decisionTTL.Seconds())
	for _, f := range h.workers {
		s.Workers = append(s.Workers, f)
	}
	return s
}
func (h *Hub) Replay(cursor string) ([]Event, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expireLocked(time.Now())
	if cursor == h.cursor() {
		return nil, true
	}
	if !strings.HasPrefix(cursor, h.epoch+":") {
		return nil, false
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(cursor, h.epoch+":"), 10, 64)
	if err == nil && n <= h.seq && h.seq-n <= uint64(len(h.events)) {
		h.subscriberLag = h.seq - n
		return append([]Event(nil), h.events[len(h.events)-int(h.seq-n):]...), true
	}
	return nil, false
}
func (h *Hub) Decision(id string) (Decision, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.decisions[id]
	return d, ok && time.Since(d.At) <= h.decisionTTL
}
func (h *Hub) History() []Summary {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []Summary{}
	for _, s := range h.history {
		if time.Since(s.At) <= time.Hour {
			out = append(out, s)
		}
	}
	return out
}
func (h *Hub) Subscribe() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscribers >= 4 {
		return false
	}
	h.subscribers++
	return true
}
func (h *Hub) Unsubscribe() { h.mu.Lock(); h.subscribers--; h.mu.Unlock() }
func (h *Hub) RecordSnapshot(duration time.Duration, size int) {
	h.mu.Lock()
	h.snapshotNanos = duration.Nanoseconds()
	h.snapshotBytes = size
	h.mu.Unlock()
}
func (h *Hub) Metrics(w io.Writer) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintf(w, "relayweave_topology_snapshot_duration_seconds %g\nrelayweave_topology_snapshot_size_bytes %d\nrelayweave_topology_delivery_delay_seconds %g\nrelayweave_topology_subscriber_lag_frames %d\nrelayweave_topology_decisions_stored %d\nrelayweave_topology_subscribers %d\n", float64(h.snapshotNanos)/1e9, h.snapshotBytes, h.deliveryDelay, h.subscriberLag, len(h.decisions), h.subscribers)
}
