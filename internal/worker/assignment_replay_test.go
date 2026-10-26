package worker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"proxyplatform/internal/config"
	"proxyplatform/internal/wire"
)

func TestLateAssignmentCannotReapplyDurableGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	a := &Agent{ctx: ctx, dir: dir, runtime: NewRuntime(ctx, filepath.Join(dir, "runtime"))}
	if err := a.load(); err != nil {
		t.Fatal(err)
	}
	c := config.Config{Managed: true, StateDir: filepath.Join(dir, "runtime"), MetricsListen: "127.0.0.1:0", ProbeURL: "http://example.invalid/health", UDP: config.UDP{Bind: "127.0.0.1", Advertise: "127.0.0.1", PortMin: 31000, PortMax: 31000}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	a.state.Generation = 7
	a.state.FenceGeneration = 7
	a.state.Config = &c
	a.state.Constraints = &c
	if err := a.saveLocked(); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(dir, "worker-meta.enc")
	before, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	// A late Sync response must not revisit storage/artifact preparation for an
	// already durable revision, even if that preparation is now unavailable.
	if err := os.Mkdir(filepath.Join(dir, "approved-artifacts.json"), 0700); err != nil {
		t.Fatal(err)
	}
	a.failed.Store(9)
	for _, generation := range []int64{7, 6} {
		a.applying.Store(generation)
		a.apply(wire.Assignment{Generation: generation, Config: &c})
		if a.state.Generation != 7 || a.state.FenceGeneration != 7 || a.failed.Load() != 9 || a.applying.Load() != 0 {
			t.Fatalf("late assignment %d changed accepted or failed generation: accepted=%d fence=%d failed=%d applying=%d", generation, a.state.Generation, a.state.FenceGeneration, a.failed.Load(), a.applying.Load())
		}
		after, err := os.ReadFile(metaPath)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("late assignment %d rewrote the durable journal: %v", generation, err)
		}
	}
	// A newer restrictive attempt may have failed after advancing its fence.
	// An intermediate late response must preserve that newer failure marker.
	a.state.FenceGeneration = 9
	a.applying.Store(8)
	a.apply(wire.Assignment{Generation: 8, Config: &c})
	if a.state.Generation != 7 || a.state.FenceGeneration != 9 || a.failed.Load() != 9 || a.applying.Load() != 0 {
		t.Fatal("late intermediate assignment replaced the newer revocation failure")
	}
	after, err := os.ReadFile(metaPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("late intermediate assignment rewrote the durable journal: %v", err)
	}
}
