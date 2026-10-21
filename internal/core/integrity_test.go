package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"proxyplatform/internal/config"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDigestCheckedBeforeVersionCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake-xray")
	os.WriteFile(path, []byte("#!/bin/sh\nprintf x > \"$0.ran\"\nprintf 'Xray 42\\n'\n"), 0755)
	_, e := New(config.Config{Cores: []config.Core{{ID: "c", Version: "42", Binary: path, SHA256: strings.Repeat("0", 64)}}})
	if e == nil {
		t.Fatal("wrong binary digest accepted")
	}
	if _, e := os.Stat(path + ".ran"); !os.IsNotExist(e) {
		t.Fatal("unverified binary executed for version inspection")
	}
}

func TestChangedBinaryNeverRestarts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-xray")
	body := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then printf 'Xray 42\\n'; exit 0; fi\nif [ \"$2\" = -test ]; then exit 0; fi\nexec sleep 30\n")
	if e := os.WriteFile(path, body, 0755); e != nil {
		t.Fatal(e)
	}
	hash, _ := FileHash(path)
	cfg := config.Config{StateDir: filepath.Join(dir, "state"), Cores: []config.Core{{ID: "c", Version: "42", Binary: path, SHA256: hash}}, Routes: []config.Route{{ID: "r", Core: "c", Endpoint: "127.0.0.1:1", Outbound: []byte(`{"protocol":"blackhole"}`)}}}
	m, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if e = m.Start(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	instance := m.Instances["c"]
	deadline := time.Now().Add(3 * time.Second)
	for instance.PID.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	pid := instance.PID.Load()
	if pid < 2 {
		t.Fatal("fixture process did not start")
	}
	fixtureReady := false
	for time.Now().Before(deadline) {
		name, _ := os.ReadFile(filepath.Join("/proc", fmt.Sprint(pid), "comm"))
		if strings.TrimSpace(string(name)) == "sleep" {
			fixtureReady = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !fixtureReady {
		t.Fatal("fixture did not exec its initial verified command")
	}
	if e = os.WriteFile(path, []byte("#!/bin/sh\nprintf changed > \"$0.ran\"\n"), 0755); e != nil {
		t.Fatal(e)
	}
	syscall.Kill(-int(pid), syscall.SIGKILL)
	done := make(chan struct{})
	go func() { m.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("digest mismatch did not stop restart loop")
	}
	if instance.Starts.Load() != 1 {
		t.Fatal("changed binary restarted")
	}
	if _, e = os.Stat(path + ".ran"); !os.IsNotExist(e) {
		t.Fatal("changed binary executed")
	}
}
