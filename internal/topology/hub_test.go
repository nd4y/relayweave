package topology

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func frame(n uint64) Frame {
	return Frame{Epoch: "worker-epoch", Sequence: n, At: time.Now(), Routes: []Route{{ID: "profile", Active: 5, Up: 100, Down: 1000}}}
}
func TestSnapshotReplayEpochAndDuplicate(t *testing.T) {
	h := NewHub()
	start := h.Snapshot()
	if !h.Accept("worker-1", frame(1)) {
		t.Fatal("accept")
	}
	events, ok := h.Replay(start.Cursor)
	if !ok || len(events) != 1 {
		t.Fatalf("snapshot cursor must replay first event: %v %v", ok, events)
	}
	if h.Accept("worker-1", frame(1)) {
		t.Fatal("duplicate accepted")
	}
	cursor := h.Snapshot().Cursor
	for i := 2; i < 36; i++ {
		h.Accept("worker-1", frame(uint64(i)))
	}
	if _, ok = h.Replay(cursor); ok {
		t.Fatal("gap must resync")
	}
	if _, ok = h.Replay("other:1"); ok {
		t.Fatal("epoch must resync")
	}
	f := frame(1)
	f.Epoch = "restarted"
	if !h.Accept("worker-1", f) {
		t.Fatal("restart")
	}
}
func TestBoundsExpiryAndPrivacy(t *testing.T) {
	h := NewHub()
	f := frame(1)
	f.Routes = make([]Route, MaxRoutes+1)
	if h.Accept("worker-1", f) {
		t.Fatal("oversize")
	}
	f = frame(1)
	f.Decisions = []Decision{{ID: "fresh", At: time.Now(), Reason: "selected_by_policy"}, {ID: "old", At: time.Now().Add(-time.Hour)}}
	h.Accept("worker-1", f)
	if _, ok := h.Decision("old"); ok {
		t.Fatal("expiry")
	}
	if _, ok := h.Decision("fresh"); !ok {
		t.Fatal("missing")
	}
	for i := 0; i < 4; i++ {
		if !h.Subscribe() {
			t.Fatal("subscription")
		}
	}
	if h.Subscribe() {
		t.Fatal("subscriber cap")
	}
	h.Unsubscribe()
	if !h.Subscribe() {
		t.Fatal("release")
	}
	b, _ := json.Marshal(h.Snapshot())
	if len(b) > 1<<20 {
		t.Fatal("size")
	}
}
func TestObserverNeverBlocksAndBounds(t *testing.T) {
	o := NewObserver()
	for i := 0; i < 10000; i++ {
		o.Offer(Decision{Reason: "error"})
	}
	f := frame(1)
	o.Fill(&f)
	if len(f.Decisions) != 128 || f.Dropped != 9872 {
		t.Fatalf("%d %d", len(f.Decisions), f.Dropped)
	}
}
func TestConcurrentSnapshot(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 1; n <= 100; n++ {
				h.Accept(fmt.Sprint("worker-", i), frame(uint64(n)))
				h.Snapshot()
				h.History()
			}
		}(i)
	}
	wg.Wait()
}

func TestWatchAcceptanceReplayAndDuplicate(t *testing.T) {
	h := NewHub()
	cursor := h.Snapshot().Cursor
	waiting := h.Watch()
	if !h.Accept("worker", frame(1)) {
		t.Fatal("accept")
	}
	select {
	case <-waiting:
	default:
		t.Fatal("accepted frame did not wake watcher")
	}
	events, ok := h.Replay(cursor)
	if !ok || len(events) != 1 {
		t.Fatal("watch before replay lost frame")
	}
	waiting = h.Watch()
	if h.Accept("worker", frame(1)) {
		t.Fatal("duplicate accepted")
	}
	select {
	case <-waiting:
		t.Fatal("duplicate woke watcher")
	default:
	}
	if !h.Accept("worker", frame(2)) {
		t.Fatal("next accept")
	}
	select {
	case <-waiting:
	default:
		t.Fatal("second generation did not wake")
	}
}
