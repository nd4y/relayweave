package worker

import (
	"context"
	"os"
	"path/filepath"
	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"testing"
)

func TestArtifactCacheRetainsPreviousAndRunningSelectors(t *testing.T) {
	a := &Agent{ctx: context.Background(), dir: t.TempDir()}
	if err := a.load(); err != nil {
		t.Fatal(err)
	}
	a.state.Generation = 1
	a.state.Config = &config.Config{Cores: []config.Core{{Version: "26.9.30", Artifact: "26.9.30-previous"}}}
	if err := a.saveLocked(); err != nil {
		t.Fatal(err)
	}
	a.state.Generation = 2
	a.state.Config = &config.Config{Cores: []config.Core{{Version: "26.9.30", Artifact: "26.9.30-current"}}}
	if err := a.saveLocked(); err != nil {
		t.Fatal(err)
	}
	a.runtime = &Runtime{entries: map[string]*engineEntry{"draining": {route: &Route{Instance: &core.Instance{Config: config.Core{Version: "26.9.30", Artifact: "26.9.30-draining"}}}}}}
	protected, err := a.protectedArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"26.10.17", "26.9.30-previous", "26.9.30-current", "26.9.30-draining"} {
		if len(protected[selector]) == 0 {
			t.Fatalf("unprotected %s", selector)
		}
	}
	if err := os.WriteFile(filepath.Join(a.dir, "previous-good.enc"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.protectedArtifacts(); err == nil {
		t.Fatal("cache maintenance allowed with unreadable recovery state")
	}
	a.applying.Store(1)
	if _, err := a.cacheCommand(context.Background(), "core-cache", nil); err == nil || err.Error() != "artifact_cache_apply_in_progress" {
		t.Fatalf("apply guard: %v", err)
	}
}
