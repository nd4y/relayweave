package worker

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"proxyplatform/internal/config"
	"proxyplatform/internal/wire"
)

type admissionFixture struct {
	mu        sync.Mutex
	guard     config.LiveGuard
	seen      atomic.Int64
	syncCalls atomic.Int64
}

func (s *admissionFixture) Sync(ctx context.Context, _ *wire.Hello) (*wire.Assignment, error) {
	s.syncCalls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s *admissionFixture) Admission(_ context.Context, request *wire.AdmissionRequest) (*wire.AdmissionResponse, error) {
	if request.WorkerID != "worker-test" {
		return nil, status.Error(codes.PermissionDenied, "wrong fixture worker")
	}
	s.seen.Store(request.SeenVersion)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.guard.Version <= request.SeenVersion {
		return &wire.AdmissionResponse{}, nil
	}
	copy := s.guard.Clone()
	return &wire.AdmissionResponse{LiveGuard: &copy}, nil
}

func TestAdmissionReceivesSuccessiveRevocationsWhileJournalIsBlocked(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	fixture := &admissionFixture{guard: guardFixture(1)}
	delete(fixture.guard.Routes, "a")
	server := grpc.NewServer()
	wire.Register(server, fixture)
	go server.Serve(listener)
	defer server.Stop()
	// Only this in-process wire fixture uses insecure transport. The real
	// contention acceptance test exercises the production mTLS connection.
	conn, err := grpc.NewClient("passthrough:///admission-test", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	a := &Agent{workerID: "worker-test", dir: t.TempDir(), runtime: guardRuntime()}
	if err := a.load(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.mu.Lock()
	locked := true
	done := make(chan struct{})
	defer func() {
		if locked {
			a.mu.Unlock()
		}
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("admission cleanup did not finish")
		}
	}()
	go func() {
		a.admissionLoop(ctx, func(call context.Context, request *wire.AdmissionRequest) (*wire.AdmissionResponse, error) {
			return wire.Admission(call, conn, request)
		}, 10*time.Millisecond)
		close(done)
	}()
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				t.Fatal("admission change waited for the journal or status path")
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait(func() bool { p := a.runtime.Candidates("policy:p"); return len(p) == 1 && p[0].Config.ID == "b" })
	fixture.mu.Lock()
	fixture.guard = config.LiveGuard{Version: 2}
	fixture.mu.Unlock()
	wait(func() bool { return len(a.runtime.Candidates("policy:p")) == 0 && fixture.seen.Load() == 2 })
	if a.persistedGuardVersion != 0 || fixture.syncCalls.Load() != 0 {
		t.Fatal("undurable version acknowledged or blocked status path used")
	}
	// Cancellation must drain the latest pending restriction after the blocked
	// writer is released, even if its normal retry tick has not run.
	flushStarted := time.Now()
	cancel()
	a.mu.Unlock()
	locked = false
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("admission loops did not flush and stop")
	}
	t.Logf("latest encrypted journal flush after release: %s", time.Since(flushStarted))
	if a.persistedGuardVersion != 2 {
		t.Fatal("latest pending restriction was not durably acknowledged")
	}
	b := &Agent{dir: a.dir}
	if err := b.load(); err != nil {
		t.Fatal(err)
	}
	if b.loadedGuard == nil || b.loadedGuard.Version != 2 || len(b.loadedGuard.Routes) != 0 {
		t.Fatal("latest coalesced restriction did not survive restart")
	}
}
