package compute

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func planFixture() Snapshot {
	set, _ := Hash([]string{"a", "b"})
	return Snapshot{Version: 1, Installation: "installation", Executor: "worker-1", Policy: "policy", Boot: "boot", Sequence: 1, Generation: 1, RouteSet: set, Objective: "objective-v1", Groups: []Group{
		{ID: "a", Lane: "lane", Baseline: 500000, RTTMS: 20, Samples: 100, CapacityBPS: 10000000},
		{ID: "b", Lane: "lane", Baseline: 500000, RTTMS: 1000, Samples: 100, CapacityBPS: 10000000},
	}}
}

func TestSearchPartitionsAndIndependentVerification(t *testing.T) {
	s := planFixture()
	all, e := Generate(s)
	if e != nil {
		t.Fatal(e)
	}
	full, e := Search(context.Background(), s, 0, uint32(len(all)))
	if e != nil || full.ID == 0 {
		t.Fatal("missing improvement", e, full)
	}
	if Verify(context.Background(), s, full) != nil {
		t.Fatal("good candidate rejected")
	}
	for _, bounds := range [][2]uint32{{0, uint32(len(all) / 2)}, {uint32(len(all) / 2), uint32(len(all) - len(all)/2)}} {
		part, e := Search(context.Background(), s, bounds[0], bounds[1])
		if e != nil {
			t.Fatal(e)
		}
		if part.ID != 0 && (part.ID < bounds[0] || part.ID >= bounds[0]+bounds[1]) {
			t.Fatal("escaped task range")
		}
	}
	full.Weights[0]++
	if Verify(context.Background(), s, full) == nil {
		t.Fatal("tampered weights accepted")
	}
	s.Groups[1].Lane = "other"
	blocked, e := Search(context.Background(), s, 0, uint32(len(all)))
	if e != nil || blocked.ID != 0 {
		t.Fatal("unknown shared ISP capacity invented", e)
	}
}

func TestUnknownCapacityAndCancelledSearch(t *testing.T) {
	s := planFixture()
	s.Groups[1].CapacityBPS = 0
	all, e := Generate(s)
	if e != nil || len(all) != 1 {
		t.Fatal("unknown capacity optimized", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = Search(ctx, planFixture(), 0, 64); e == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestCacheFreshnessEpochReplayAndKeyRevocation(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	cache := NewCache(map[string]ed25519.PublicKey{"key": pub})
	now := time.Second
	cache.Now = func() (time.Duration, error) { return now, nil }
	cache.SetEpoch(1)
	s, e := cache.Capture(planFixture())
	if e != nil {
		t.Fatal(e)
	}
	candidate, e := Search(context.Background(), s, 0, 64)
	if e != nil {
		t.Fatal(e)
	}
	h, _ := Hash(s)
	r, e := Sign(Recommendation{Version: 1, KeyID: "key", OwnerEpoch: 1, Sequence: 1, Snapshot: s, SnapshotDigest: h, Candidate: candidate, BlendPPM: 100000}, key)
	if e != nil {
		t.Fatal(e)
	}
	w, e := cache.Accept(context.Background(), r, 1, s.RouteSet)
	if e != nil || len(w) != 2 || w[0]+w[1] != PPM {
		t.Fatal("valid plan rejected", e)
	}
	if _, e = cache.Accept(context.Background(), r, 1, s.RouteSet); e == nil {
		t.Fatal("replay accepted")
	}
	if _, e = cache.Accept(context.Background(), r, 2, s.RouteSet); e == nil {
		t.Fatal("generation bypass")
	}
	now += 31 * time.Second
	if cache.Fresh(s.Policy, s.Sequence) {
		t.Fatal("suspend/expiry ignored")
	}
	r.Sequence = 2
	r, _ = Sign(r, key)
	if _, e = cache.Accept(context.Background(), r, 1, s.RouteSet); e == nil {
		t.Fatal("expired plan accepted")
	}
	cache.SetKeys(nil)
	if cache.Fresh(s.Policy, s.Sequence) {
		t.Fatal("key revocation retained plan")
	}
}

func TestNewSnapshotCannotRefreshOlderPlan(t *testing.T) {
	cache := NewCache(nil)
	now := time.Second
	cache.Now = func() (time.Duration, error) { return now, nil }
	first, _ := cache.Capture(planFixture())
	now += time.Second
	_, _ = cache.Capture(planFixture())
	if cache.Fresh(first.Policy, first.Sequence) {
		t.Fatal("new observations refreshed old recommendation")
	}
}
