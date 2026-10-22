package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"proxyplatform/internal/config"
	"proxyplatform/internal/wire"
	"testing"
	"time"
)

func TestV1JournalMigratesWithoutDowngradeOrFenceReset(t *testing.T) {
	a := &Agent{dir: t.TempDir()}
	if err := a.load(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(time.Hour)
	old := savedState{Generation: 7, FenceGeneration: 9, GuardVersion: 11, Config: &config.Config{LeaseUntil: deadline}, Constraints: &config.Config{Managed: true}, Done: map[string]wire.Result{"consumed": {ID: "consumed", Success: true}}}
	meta := mutableState{Generation: 7, Fence: 9, GuardVersion: 11, Lease: deadline, Done: old.Done, ProbeDay: time.Now().UTC().Truncate(24 * time.Hour), ProbeBudget: map[string]int64{"p": 12345}}
	guard := config.LiveGuard{Version: 11, Routes: map[string]config.RoutePermit{}}
	for _, item := range []struct {
		name, domain string
		data         any
	}{{"last-good.enc", "relayweave-worker-v1", old}, {"worker-meta.enc", "relayweave-meta-v1", meta}, {"live-guard.enc", "relayweave-live-guard-v1", guard}} {
		plain, err := json.Marshal(item.data)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := a.sealWorkerPayload(plain, item.domain)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(a.dir, item.name), sealed, 0600); err != nil {
			t.Fatal(err)
		}
	}
	b := &Agent{dir: a.dir}
	if err := b.load(); err != nil {
		t.Fatal(err)
	}
	if !b.legacyJournal || b.state.Generation != 7 || b.state.FenceGeneration != 9 || b.state.GuardVersion != 11 || !b.state.Done["consumed"].Success || b.state.ProbeBudget["p"] != 12345 || !b.state.Config.LeaseUntil.Equal(deadline) {
		t.Fatal("migration lost authority, consumed action or budget state")
	}
	if err := b.saveLocked(); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ name, legacy string }{{"last-good.enc", "relayweave-worker-v1"}, {"previous-good.enc", "relayweave-worker-v1"}, {"worker-meta.enc", "relayweave-meta-v1"}} {
		raw, err := os.ReadFile(filepath.Join(a.dir, item.name))
		if err != nil {
			t.Fatal(err)
		}
		n := a.aead.NonceSize()
		if _, err = a.aead.Open(nil, raw[:n], raw[n:], []byte(item.legacy)); err == nil {
			t.Fatal("old reader silently accepted v3 contract", item.name)
		}
	}
	c := &Agent{dir: a.dir}
	if err := c.load(); err != nil {
		t.Fatal(err)
	}
	if c.legacyJournal || c.state.RoutingVersion != wire.RoutingVersion || c.state.FenceGeneration != 9 || !c.state.Done["consumed"].Success || c.state.ProbeBudget["p"] != 12345 {
		t.Fatal("v3 reload changed security state")
	}
	if err := os.WriteFile(filepath.Join(a.dir, "last-good.enc"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	c = &Agent{dir: a.dir}
	if err := c.load(); err != nil {
		t.Fatal(err)
	}
	if c.state.FenceGeneration < 9 || c.state.GuardVersion < 11 || !c.state.Done["consumed"].Success {
		t.Fatal("resealed fallback lowered a fence")
	}
}

func TestFutureRoutingContractIsNotSilentlyDowngraded(t *testing.T) {
	a := &Agent{dir: t.TempDir()}
	if err := a.load(); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"last-good.enc", "worker-meta.enc"} {
		domain := workerStateDomain
		if filename == "worker-meta.enc" {
			domain = workerMetaDomain
		}
		plain, _ := json.Marshal(map[string]any{"routing_version": wire.RoutingVersion + 1, "generation": 8, "fence": 9})
		sealed, err := a.sealWorkerPayload(plain, domain)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(a.dir, filename), sealed, 0600); err != nil {
			t.Fatal(err)
		}
		b := &Agent{dir: a.dir}
		if err = b.load(); err == nil {
			t.Fatal("future security contract accepted", filename)
		}
		if err = os.Remove(filepath.Join(a.dir, filename)); err != nil {
			t.Fatal(err)
		}
	}
}
