package worker

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxyplatform/internal/config"
)

func guardFixture(version int64) config.LiveGuard {
	return config.LiveGuard{Version: version, Routes: map[string]config.RoutePermit{
		"a": {Fingerprint: strings.Repeat("a", 64), ValidUntil: time.Now().UTC().Add(time.Hour)},
		"b": {Fingerprint: strings.Repeat("b", 64), ValidUntil: time.Now().UTC().Add(time.Hour)},
	}}
}

func guardRuntime() *Runtime {
	r := selectionFixture("round-robin")
	r.current["a"].route.Config.Fingerprint = strings.Repeat("a", 64)
	r.current["b"].route.Config.Fingerprint = strings.Repeat("b", 64)
	return r
}

func TestLiveGuardRevocationsSurviveStaleMessagesAndConfigurationRollback(t *testing.T) {
	r := guardRuntime()
	g := guardFixture(1)
	delete(g.Routes, "a")
	r.RestrictLiveGuard(g)
	g.Routes["a"] = config.RoutePermit{Fingerprint: strings.Repeat("a", 64), ValidUntil: time.Now().Add(time.Hour)}
	if paths := r.Candidates("policy:p"); len(paths) != 1 || paths[0].Config.ID != "b" {
		t.Fatal("guard was not isolated from caller mutation")
	}
	r.RestrictLiveGuard(config.LiveGuard{Version: 2})
	r.RestrictLiveGuard(guardFixture(1))
	r.Restrict(config.Config{Routes: []config.Route{r.current["a"].route.Config, r.current["b"].route.Config}, Policies: []config.Policy{{ID: "p", Routes: []string{"a", "b"}}}})
	if len(r.Candidates("policy:p")) != 0 {
		t.Fatal("old guard or configuration rollback restored a revoked route")
	}
	r.RestrictLiveGuard(guardFixture(3))
	if len(r.Candidates("policy:p")) != 2 {
		t.Fatal("newer eligible catalogue was ignored")
	}
	r.valid["a"] = time.Now().Add(-time.Second)
	g = guardFixture(4)
	g.Routes["b"] = config.RoutePermit{Fingerprint: strings.Repeat("c", 64), ValidUntil: time.Now().Add(time.Hour)}
	r.RestrictLiveGuard(g)
	if len(r.Candidates("policy:p")) != 0 {
		t.Fatal("fresh guard bypassed accepted TTL or a credential change")
	}
	delete(r.valid, "a")
	g = guardFixture(5)
	g.Routes["a"] = config.RoutePermit{Fingerprint: strings.Repeat("a", 64), ValidUntil: time.Now().Add(-time.Second)}
	delete(g.Routes, "b")
	r.RestrictLiveGuard(g)
	if len(r.Candidates("policy:p")) != 0 {
		t.Fatal("expired guard permit admitted new traffic")
	}
}

func TestLiveGuardDurabilityAndFallbackCannotUseAnOlderFence(t *testing.T) {
	a := &Agent{dir: t.TempDir(), runtime: guardRuntime()}
	if err := a.load(); err != nil {
		t.Fatal(err)
	}
	if err := a.acceptLiveGuard(guardFixture(1)); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(filepath.Join(a.dir, "live-guard.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(old, []byte(strings.Repeat("a", 64))) {
		t.Fatal("guard stored without encryption")
	}
	a.state.Generation, a.state.FenceGeneration = 1, 1
	if err := a.saveLocked(); err != nil {
		t.Fatal(err)
	}
	g := guardFixture(2)
	delete(g.Routes, "a")
	if err := a.acceptLiveGuard(g); err != nil {
		t.Fatal(err)
	}
	b := &Agent{dir: a.dir}
	if err := b.load(); err != nil {
		t.Fatal(err)
	}
	if b.persistedGuardVersion != 2 || b.loadedGuard == nil || len(b.loadedGuard.Routes) != 1 {
		t.Fatal("latest fence not restored")
	}
	current, err := os.ReadFile(filepath.Join(a.dir, "live-guard.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.saveLocked(); err != nil {
		t.Fatal(err)
	}
	same, _ := os.ReadFile(filepath.Join(a.dir, "live-guard.enc"))
	if !bytes.Equal(current, same) {
		t.Fatal("metadata write rewrote the catalogue fence")
	}
	for _, fault := range []string{"stale", "corrupt", "missing"} {
		t.Run(fault, func(t *testing.T) {
			path := filepath.Join(a.dir, "live-guard.enc")
			switch fault {
			case "stale":
				os.WriteFile(path, old, 0600)
			case "corrupt":
				os.WriteFile(path, []byte("bad"), 0600)
			case "missing":
				os.Remove(path)
			}
			if err := (&Agent{dir: a.dir}).load(); err == nil {
				t.Fatal("unsafe fence fallback accepted")
			}
			if err := os.WriteFile(path, current, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLiveGuardRestrictsBeforeJournalMutexAndNeverAcknowledgesFailedWrite(t *testing.T) {
	a := &Agent{dir: t.TempDir(), runtime: guardRuntime()}
	if err := a.load(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(a.dir, "live-guard.enc"), 0700); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- a.acceptLiveGuard(config.LiveGuard{Version: 1}) }()
	deadline := time.Now().Add(3 * time.Second)
	for len(a.runtime.Candidates("policy:p")) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	blocked := len(a.runtime.Candidates("policy:p")) == 0
	a.mu.Unlock()
	if !blocked {
		t.Fatal("revocation waited for a stalled journal mutex")
	}
	if err := <-done; err == nil || a.persistedGuardVersion != 0 {
		t.Fatal("failed write acknowledged")
	}
	if err := (&Agent{dir: a.dir}).load(); err == nil {
		t.Fatal("missing newer durable fence did not fail closed")
	}
}
