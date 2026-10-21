package worker

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"proxyplatform/internal/config"
	"proxyplatform/internal/store"
)

func (r *Runtime) RestrictLiveGuard(guard config.LiveGuard) {
	copy := guard.Clone()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.liveGuard == nil || copy.Version > r.liveGuard.Version {
		r.liveGuard = &copy
	}
}

// Restrict before waiting for the journal mutex or any filesystem operation.
// Acknowledgement advances only after both the high-water mark and encrypted
// snapshot are durable. Old Apply/rollback generations cannot clear this fence.
func (a *Agent) acceptLiveGuard(guard config.LiveGuard) error {
	if err := guard.Validate(); err != nil {
		return err
	}
	a.runtime.RestrictLiveGuard(guard)
	a.mu.Lock()
	defer a.mu.Unlock()
	if guard.Version < a.state.GuardVersion || guard.Version <= a.persistedGuardVersion {
		return nil
	}
	a.state.GuardVersion = guard.Version
	if err := a.saveMeta(); err != nil {
		return err
	}
	plain, err := json.Marshal(guard)
	if err != nil {
		return err
	}
	nonce := make([]byte, a.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	sealed := a.aead.Seal(nonce, nonce, plain, []byte("relayweave-live-guard-v1"))
	if err = store.AtomicFile(filepath.Join(a.dir, "live-guard.enc"), sealed, 0600); err != nil {
		return err
	}
	a.persistedGuardVersion = guard.Version
	return nil
}

func (a *Agent) loadLiveGuard() error {
	b, err := readBounded(filepath.Join(a.dir, "live-guard.enc"), 8<<20)
	if os.IsNotExist(err) && a.state.GuardVersion == 0 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("live_guard_unavailable")
	}
	n := a.aead.NonceSize()
	if len(b) <= n {
		return fmt.Errorf("live_guard_corrupt")
	}
	plain, err := a.aead.Open(nil, b[:n], b[n:], []byte("relayweave-live-guard-v1"))
	if err != nil {
		return fmt.Errorf("live_guard_corrupt")
	}
	var guard config.LiveGuard
	if json.Unmarshal(plain, &guard) != nil || guard.Validate() != nil || guard.Version < a.state.GuardVersion {
		return fmt.Errorf("live_guard_corrupt")
	}
	a.loadedGuard = &guard
	a.state.GuardVersion = guard.Version
	a.persistedGuardVersion = guard.Version
	return nil
}

func (a *Agent) loadJournals() error {
	if err := a.loadMeta(); err != nil {
		return err
	}
	return a.loadLiveGuard()
}
