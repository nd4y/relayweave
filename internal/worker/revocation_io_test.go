package worker

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"proxyplatform/internal/config"
	"proxyplatform/internal/wire"
)

func TestRevocationBlocksNewTrafficWhileCheckpointIOIsStalled(t *testing.T) {
	a := &Agent{dir: t.TempDir(), ctx: context.Background(), runtime: selectionFixture("fallback")}
	if err := a.load(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.dir, "last-good.enc")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// Hold a real checkpoint read pending until the test explicitly releases it.
	hold, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// Force a diagnosed persistence failure after releasing the stalled read;
	// no engine install or gateway setup is needed for this restrictive change.
	if err := os.Mkdir(filepath.Join(a.dir, "worker-meta.enc"), 0700); err != nil {
		hold.Close()
		t.Fatal(err)
	}
	c := config.Config{Managed: true, StateDir: filepath.Join(a.dir, "runtime"), MetricsListen: "127.0.0.1:0", ProbeURL: "http://example.invalid/health", UDP: config.UDP{Bind: "127.0.0.1", Advertise: "127.0.0.1", PortMin: 31000, PortMax: 31000}}
	if err := c.Validate(); err != nil {
		hold.Close()
		t.Fatal(err)
	}
	var released sync.Once
	release := func() {
		released.Do(func() {
			// Unlink before closing the writer so a reader that has not opened
			// yet cannot block forever waiting for a writer during cleanup.
			os.Remove(path)
			hold.Close()
		})
	}
	done := make(chan struct{})
	go func() { defer close(done); a.apply(wire.Assignment{Generation: 2, Config: &c}) }()
	defer func() {
		release()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("checkpoint operation did not finish after releasing its I/O")
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for len(a.runtime.Candidates("policy:p")) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("new traffic remained permitted behind stalled checkpoint I/O")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Confirm the actual checkpoint reader has reached the controlled fault;
	// observing the in-memory fence alone is not evidence of stalled file I/O.
	readerOpen := func() bool {
		files, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if file.Name() == strconv.Itoa(int(hold.Fd())) {
				continue
			}
			if target, _ := os.Readlink("/proc/self/fd/" + file.Name()); target == path {
				return true
			}
		}
		return false
	}
	deadline = time.Now().Add(15 * time.Second)
	for !readerOpen() {
		if time.Now().After(deadline) {
			t.Fatal("checkpoint reader did not reach the controlled I/O fault")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("persistence unexpectedly completed while checkpoint I/O was blocked")
	default:
	}
	release()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("persistence failure was not reported after releasing its I/O")
	}
	if a.failed.Load() != 2 || len(a.runtime.Candidates("policy:p")) != 0 {
		t.Fatal("failed persistence acknowledged or broadened the restrictive change")
	}
}
