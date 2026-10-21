package topology

import (
	"fmt"
	"testing"
	"time"
)

func TestDetailBudgetAndExpiryAlsoRemoveOfflineFrameDetails(t *testing.T) {
	h := NewHub()
	h.decisionLimit = 64
	for i := 1; i <= 8; i++ {
		f := frame(uint64(i))
		for n := 0; n < 32; n++ {
			f.Decisions = append(f.Decisions, Decision{ID: fmt.Sprintf("%d-%d", i, n), At: time.Now()})
		}
		h.Accept("worker-1", f)
	}
	if len(h.decisions) != 64 || len(h.order) != 64 {
		t.Fatal("detail budget")
	}
	h.decisionTTL = time.Nanosecond
	h.Expire()
	if len(h.decisions) != 0 || len(h.order) != 0 {
		t.Fatal("expired detail retained")
	}
	snapshot := h.Snapshot()
	if len(snapshot.Workers[0].Decisions) != 0 {
		t.Fatal("offline frame retained expired detail")
	}
	for _, e := range h.events {
		if len(e.Frame.Decisions) != 0 {
			t.Fatal("replay retained expired detail")
		}
	}
}
func TestStringAndDNSBudgets(t *testing.T) {
	h := NewHub()
	f := frame(1)
	f.Routes[0].ServerIPs = []string{"https://credential.invalid/?secret=forbidden"}
	if h.Accept("worker-1", f) {
		t.Fatal("non IP accepted")
	}
	f = frame(1)
	f.Decisions = []Decision{{ID: "id", Candidates: make([]Candidate, 33)}}
	if h.Accept("worker-1", f) {
		t.Fatal("candidate cap")
	}
}
