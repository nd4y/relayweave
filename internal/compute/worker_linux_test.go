package compute

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"proxyplatform/internal/identity"
)

// This runs the real worker entrypoint (including cgroup verification), its
// network loop, and the isolated evaluator child under the build container cap.
func TestWorkerCLIConstrainedLifecycle(t *testing.T) {
	binary := os.Getenv("RW_COMPUTE_WORKER_BINARY")
	if binary == "" {
		t.Skip("build-01 compiled worker fixture required")
	}
	r := testRepository(t)
	setup := context.Background()
	var e error
	dir := t.TempDir()
	if e = Bootstrap(dir); e != nil {
		t.Fatal(e)
	}
	peer := t.TempDir()
	if e = Provision(setup, r, dir, peer, "compute-peer"); e != nil {
		t.Fatal(e)
	}
	seed, e := ReadKey(filepath.Join(dir, "signing.seed"))
	if e != nil {
		t.Fatal(e)
	}
	controller := &Controller{Repo: r, Key: ed25519.NewKeyFromSeed(seed), KeyID: "key"}
	serverTLS, e := identity.ServerTLS(dir)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewUnstartedServer(controller)
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	epoch, e := r.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	controller.Epoch.Store(epoch)
	cmd := exec.CommandContext(ctx, binary, "-dispatcher", server.URL, "-identity-dir", peer)
	cmd.Env = []string{"GOMAXPROCS=1", "GOMEMLIMIT=128MiB"}
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-exited
		}
	}()
	for {
		select {
		case err := <-exited:
			stopped = true
			t.Fatal("worker startup", err, stderr.String())
		default:
		}
		var registered bool
		e = r.DB.QueryRow(ctx, `SELECT session<>'' FROM compute_worker WHERE id='compute-peer'`).Scan(&registered)
		if e != nil {
			t.Fatal(e)
		}
		if registered {
			break
		}
		if !sleep(ctx, 20*time.Millisecond) {
			t.Fatal("worker registration timed out")
		}
	}
	s := planFixture()
	s.Installation = r.Installation
	if e = controller.Offer(ctx, s); e != nil {
		t.Fatal(e)
	}
	for {
		published, e := controller.Latest(ctx, s.Executor)
		if e != nil {
			t.Fatal(e)
		}
		if len(published) > 0 {
			break
		}
		if !sleep(ctx, 20*time.Millisecond) {
			t.Fatal("worker CLI produced no verified result")
		}
	}
	if e = cmd.Process.Signal(syscall.SIGTERM); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-exited:
		stopped = true
		if e != nil {
			t.Fatal("unclean worker shutdown", e, stderr.String())
		}
	case <-ctx.Done():
		t.Fatal("worker did not stop")
	}
}
