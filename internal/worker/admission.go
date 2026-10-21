package worker

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/wire"
)

type admissionFetch func(context.Context, *wire.AdmissionRequest) (*wire.AdmissionResponse, error)

// Coalesce immutable snapshots without the journal mutex or unbounded workers.
func offerAdmission(pending *atomic.Pointer[config.LiveGuard], guard *config.LiveGuard) {
	for {
		old := pending.Load()
		if old != nil && old.Version >= guard.Version {
			return
		}
		if pending.CompareAndSwap(old, guard) {
			return
		}
	}
}

func (a *Agent) admissionLoop(ctx context.Context, fetch admissionFetch, interval time.Duration) {
	var pending atomic.Pointer[config.LiveGuard]
	wake := make(chan struct{}, 1)
	writes, stopWrites := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		writeFailed := false
		persist := func(guard *config.LiveGuard, retry bool) {
			if guard == nil {
				return
			}
			if err := a.acceptLiveGuard(*guard); err != nil {
				if retry {
					offerAdmission(&pending, guard)
				}
				if !writeFailed {
					slog.Error("live admission fence could not be persisted")
					writeFailed = true
				}
			} else {
				writeFailed = false
			}
		}
		for {
			select {
			case <-writes.Done():
				// The receiver has stopped offering updates. Flush its final
				// snapshot on an orderly shutdown without acknowledging failure.
				persist(pending.Swap(nil), false)
				return
			case <-wake:
			case <-ticker.C:
			}
			persist(pending.Swap(nil), true)
		}
	}()
	defer func() { stopWrites(); <-finished }()
	seen := int64(0)
	if a.loadedGuard != nil {
		seen = a.loadedGuard.Version
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		call, cancel := context.WithTimeout(ctx, 3*time.Second)
		response, err := fetch(call, &wire.AdmissionRequest{RoutingVersion: 2, WorkerID: a.workerID, SeenVersion: seen})
		cancel()
		if err == nil && response != nil && response.LiveGuard != nil {
			guard := response.LiveGuard
			if guard.Version > seen && guard.Validate() == nil {
				copy := guard.Clone()
				a.runtime.RestrictLiveGuard(copy)
				seen = copy.Version
				offerAdmission(&pending, &copy)
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
