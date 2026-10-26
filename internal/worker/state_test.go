package worker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"proxyplatform/internal/config"
	"proxyplatform/internal/wire"
	"testing"
	"time"
)

func TestEncryptedStateAndSmallLeaseWrites(t *testing.T) {
	a := &Agent{dir: t.TempDir(), ctx: context.Background()}
	if e := a.load(); e != nil {
		t.Fatal(e)
	}
	c := config.Config{LeaseUntil: time.Now().UTC().Add(time.Hour), Routes: []config.Route{{ID: "p", Outbound: []byte(`{"password":"known-private-upstream-secret"}`)}}}
	a.state.Generation = 1
	a.state.FenceGeneration = 1
	a.state.Config = &c
	a.state.Constraints = &c
	if e := a.saveLocked(); e != nil {
		t.Fatal(e)
	}
	original, e := os.ReadFile(filepath.Join(a.dir, "last-good.enc"))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(original, []byte("known-private-upstream-secret")) {
		t.Fatal("plaintext in state")
	}
	c.LeaseUntil = c.LeaseUntil.Add(time.Hour)
	if e = a.saveLocked(); e != nil {
		t.Fatal(e)
	}
	same, _ := os.ReadFile(filepath.Join(a.dir, "last-good.enc"))
	if !bytes.Equal(original, same) {
		t.Fatal("lease rewrote the entire catalog")
	}
	b := &Agent{dir: a.dir}
	if e = b.load(); e != nil {
		t.Fatal(e)
	}
	if !b.state.Config.LeaseUntil.Equal(c.LeaseUntil) {
		t.Fatal("lease not restored")
	}
	a.state.Generation = 2
	a.state.FenceGeneration = 2
	if e = a.saveLocked(); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(a.dir, "last-good.enc"), []byte("corrupt"), 0600)
	b = &Agent{dir: a.dir}
	if e = b.load(); e != nil {
		t.Fatal(e)
	}
	if b.state.Generation != 1 || b.state.FenceGeneration != 2 || b.state.Constraints == nil || len(b.state.Constraints.Routes) != 0 {
		t.Fatal("older fallback bypassed a newer revocation fence")
	}
	os.WriteFile(filepath.Join(a.dir, "worker-meta.enc"), []byte("corrupt"), 0600)
	b = &Agent{dir: a.dir}
	if e = b.load(); e == nil {
		t.Fatal("corrupt mutable state accepted")
	}
}

func TestOlderMetadataCannotReplaceNewJournal(t *testing.T) {
	a := &Agent{dir: t.TempDir()}
	if e := a.load(); e != nil {
		t.Fatal(e)
	}
	a.state.Generation = 1
	a.state.FenceGeneration = 1
	if e := a.saveLocked(); e != nil {
		t.Fatal(e)
	}
	old, _ := os.ReadFile(filepath.Join(a.dir, "worker-meta.enc"))
	a.state.Generation = 2
	a.state.FenceGeneration = 2
	a.state.Done = map[string]wire.Result{"new": {ID: "new", Success: true}}
	if e := a.saveLocked(); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(a.dir, "worker-meta.enc"), old, 0600)
	b := &Agent{dir: a.dir}
	if e := b.load(); e != nil {
		t.Fatal(e)
	}
	if !b.state.Done["new"].Success {
		t.Fatal("old journal replaced committed command results")
	}
	os.Remove(filepath.Join(a.dir, "worker.key"))
	if e := b.load(); e == nil {
		t.Fatal("missing encryption key silently replaced")
	}
}

func TestDailyProbeBudgetPersistsAcrossRestart(t *testing.T) {
	a := &Agent{dir: t.TempDir()}
	if e := a.load(); e != nil {
		t.Fatal(e)
	}
	a.runtime = &Runtime{budget: map[string]int64{}, probeWindow: time.Now().UTC().Truncate(24 * time.Hour)}
	if !a.runtime.reserveProbe(time.Now(), "p", 8<<20, 8<<20) {
		t.Fatal("first reservation")
	}
	if a.runtime.reserveProbe(time.Now(), "other", 8<<20, 8<<20) {
		t.Fatal("global ceiling bypass")
	}
	if e := a.saveLocked(); e != nil {
		t.Fatal(e)
	}
	b := &Agent{dir: a.dir}
	if e := b.load(); e != nil {
		t.Fatal(e)
	}
	r := &Runtime{budget: b.state.ProbeBudget, probeWindow: b.state.ProbeDay}
	if r.reservedProbeBudgetLocked() != 8<<20 || r.probeBytes.Load() != 0 {
		t.Fatal("durable budget must remain visible independently of restarted body counter")
	}
	if r.reserveProbe(time.Now(), "p", 8<<20, 8<<20) {
		t.Fatal("restart reset budget")
	}
	if !r.reserveProbe(time.Now().Add(24*time.Hour), "p", 8<<20, 8<<20) {
		t.Fatal("next UTC day did not reset budget")
	}
}
