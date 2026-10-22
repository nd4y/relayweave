package worker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"proxyplatform/internal/compute"
	"proxyplatform/internal/routing"
)

func computeFixture(t *testing.T) (*Runtime, ed25519.PrivateKey) {
	t.Helper()
	r := selectionFixture("auto")
	r.localV1 = true
	r.config.Generation = 1
	r.ctx = context.Background()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	r.computeCache = compute.NewCache(map[string]ed25519.PublicKey{"key": pub})
	r.computeMode = "shadow"
	r.computeInstallation = "installation"
	r.computeExecutor = "worker"
	r.computeEpoch = 1
	r.computeCache.SetEpoch(1)
	for i, v := range r.policies["p"].best {
		v.SpeedBPS.Store(10000000)
		v.LastSpeed.Store(time.Now().Unix())
		v.Instance.LatencyUS.Store(int64(20+i*980) * 1000)
		for n := 0; n < 30; n++ {
			v.localStats.Observe(time.Now().Add(time.Duration(n-30)*time.Second), float64(20+i*980), false)
		}
	}
	r.policies["p"].localPlans[0] = routing.Build([]routing.Input{{ID: "a", RTTMS: 100}, {ID: "b", RTTMS: 100}}, nil)
	return r, key
}

func TestComputeSnapshotVerificationShadowCanaryAndRevocation(t *testing.T) {
	r, key := computeFixture(t)
	snapshots := r.ComputeSnapshots()
	if len(snapshots) != 1 {
		t.Fatal("no eligible snapshot")
	}
	s := snapshots[0]
	for _, g := range s.Groups {
		if g.ID == "a" || g.ID == "b" || g.Lane == "" {
			t.Fatal("raw route identity exported")
		}
	}
	candidate, e := compute.Search(context.Background(), s, 0, 64)
	if e != nil || candidate.ID == 0 {
		t.Fatal("search", e)
	}
	h, _ := compute.Hash(s)
	rec, e := compute.Sign(compute.Recommendation{Version: 1, KeyID: "key", OwnerEpoch: 1, Sequence: 1, Snapshot: s, SnapshotDigest: h, Candidate: candidate, BlendPPM: 100000}, key)
	if e != nil {
		t.Fatal(e)
	}
	r.AcceptCompute(1, []compute.Recommendation{rec})
	p := r.policies["p"]
	if p.computePlan.N != 2 || r.computeAccepted.Load() != 1 {
		t.Fatal("valid recommendation rejected")
	}
	if r.computeEligible(p) {
		t.Fatal("shadow affected traffic")
	}
	r.computeMode = "canary"
	if !r.computeEligible(p) {
		t.Fatal("canary not eligible")
	}
	r.AcceptCompute(1, []compute.Recommendation{rec})
	if r.computeAccepted.Load() != 1 {
		t.Fatal("duplicate application")
	}
	r.computeDisabled.Store(true)
	if r.computeEligible(p) {
		t.Fatal("local stop ignored")
	}
	r.computeDisabled.Store(false)
	r.AcceptCompute(2, nil)
	if r.computeEligible(p) {
		t.Fatal("old owner epoch used")
	}
	r.AcceptCompute(0, nil)
	if r.computeEligible(p) {
		t.Fatal("lost authority retained plan")
	}
}

func TestSnapshotHashingDoesNotHoldRoutingMutex(t *testing.T) {
	r, _ := computeFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	r.computeCache.Now = func() (time.Duration, error) { close(entered); <-release; return time.Second, nil }
	go func() { defer close(finished); r.ComputeSnapshots() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("snapshot did not reach capture")
	}
	done := make(chan struct{})
	go func() { r.Candidates("policy:p"); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(release)
		<-finished
		t.Fatal("snapshot held routing mutex")
	}
	close(release)
	<-finished
}

func TestLocalComputeDisableMarkerClearsPlans(t *testing.T) {
	r, _ := computeFixture(t)
	r.computeKeyFile = filepath.Join(t.TempDir(), "keys.json")
	p := r.policies["p"]
	p.computePlan = p.localPlans[0]
	if e := os.WriteFile(r.computeKeyFile+".disabled", nil, 0600); e != nil {
		t.Fatal(e)
	}
	r.refreshComputePolicy()
	if !r.computeDisabled.Load() || p.computePlan.N != 0 {
		t.Fatal("disable did not invalidate")
	}
}
