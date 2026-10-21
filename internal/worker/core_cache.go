package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
)

func (a *Agent) protectedArtifacts() (map[string][]string, error) {
	protected := map[string][]string{"26.10.17": {"builtin"}}
	add := func(c *config.Config, reason string) {
		if c == nil {
			return
		}
		for _, v := range c.Cores {
			selector := v.Artifact
			if selector == "" {
				selector = v.Version
			}
			protected[selector] = append(protected[selector], reason)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	add(a.state.Config, "current")
	add(a.state.Constraints, "constraints")
	if a.runtime != nil {
		a.runtime.mu.RLock()
		for _, e := range a.runtime.entries {
			v := e.route.Instance.Config
			selector := v.Artifact
			if selector == "" {
				selector = v.Version
			}
			protected[selector] = append(protected[selector], "running_or_draining")
		}
		a.runtime.mu.RUnlock()
	}
	b, err := readBounded(filepath.Join(a.dir, "previous-good.enc"), 64<<20)
	if os.IsNotExist(err) {
		return protected, nil
	}
	if err != nil {
		return nil, fmt.Errorf("artifact_cache_recovery_unreadable")
	}
	if a.aead == nil || len(b) <= a.aead.NonceSize() {
		return nil, fmt.Errorf("artifact_cache_recovery_unreadable")
	}
	n := a.aead.NonceSize()
	plain, err := a.aead.Open(nil, b[:n], b[n:], []byte("relayweave-worker-v1"))
	if err != nil {
		return nil, fmt.Errorf("artifact_cache_recovery_unreadable")
	}
	var state savedState
	if json.Unmarshal(plain, &state) != nil {
		return nil, fmt.Errorf("artifact_cache_recovery_unreadable")
	}
	add(state.Config, "previous_good")
	add(state.Constraints, "previous_constraints")
	return protected, nil
}
func (a *Agent) cacheCommand(ctx context.Context, action string, payload json.RawMessage) (any, error) {
	a.artifactMu.Lock()
	defer a.artifactMu.Unlock()
	if a.applying.Load() != 0 {
		return nil, fmt.Errorf("artifact_cache_apply_in_progress")
	}
	protected, err := a.protectedArtifacts()
	if err != nil {
		return nil, err
	}
	if action == "remove-core" {
		var body struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(payload, &body) != nil {
			return nil, fmt.Errorf("invalid_core_version")
		}
		if err = core.RemoveCached(ctx, "/cores", body.Version, protected); err != nil {
			return nil, err
		}
	}
	return core.Cache("/cores", protected)
}
