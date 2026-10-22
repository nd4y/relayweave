package compute

import (
	"context"
	"errors"
	"testing"
)

func TestRejectedThirdWorkerDoesNotSpendCPU(t *testing.T) {
	r := testRepository(t)
	ctx := context.Background()
	epoch, e := r.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	ids := []string{"one", "two", "three"}
	sessions := []string{}
	for _, id := range ids {
		sessions = append(sessions, provisionTestWorker(t, r, id))
	}
	s := planFixture()
	s.Installation = r.Installation
	if e = r.Offer(ctx, epoch, s); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if _, e = r.Lease(ctx, epoch, ids[i], sessions[i], "request"); e != nil {
			t.Fatal(e)
		}
	}
	for _, request := range []string{"third", "third-retry"} {
		if _, e = r.Lease(ctx, epoch, ids[2], sessions[2], request); !errors.Is(e, ErrBudget) {
			t.Fatal("third worker admitted", e)
		}
	}
	var attempts, spent int
	if e = r.DB.QueryRow(ctx, `SELECT count(*),sum(reserved_cpu_ms) FROM compute_attempt`).Scan(&attempts, &spent); e != nil || attempts != 2 || spent != 1000 {
		t.Fatal("rejection changed reservation", attempts, spent, e)
	}
}

func TestEmptyLeaseIdempotency(t *testing.T) {
	r := testRepository(t)
	ctx := context.Background()
	epoch, e := r.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	session := provisionTestWorker(t, r, "worker")
	if _, e = r.Lease(ctx, epoch, "worker", session, "empty"); !errors.Is(e, ErrNoJob) {
		t.Fatal(e)
	}
	if e = r.RememberEmpty(ctx, epoch, "worker", "empty"); e != nil {
		t.Fatal(e)
	}
	s := planFixture()
	s.Installation = r.Installation
	if e = r.Offer(ctx, epoch, s); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Lease(ctx, epoch, "worker", session, "empty"); !errors.Is(e, ErrNoJob) {
		t.Fatal("retried empty response acquired a new job", e)
	}
	if _, e = r.Lease(ctx, epoch, "worker", session, "new-request"); e != nil {
		t.Fatal("new request starved", e)
	}
}

func TestInvalidResultsQuarantineAndWindow(t *testing.T) {
	r := testRepository(t)
	ctx := context.Background()
	epoch, e := r.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	session := provisionTestWorker(t, r, "worker")
	for i := 1; i <= 3; i++ {
		if e = r.InvalidResult(ctx, "worker"); e != nil {
			t.Fatal(e)
		}
		var revoked bool
		if e = r.DB.QueryRow(ctx, `SELECT revoked FROM compute_worker WHERE id='worker'`).Scan(&revoked); e != nil || revoked != (i == 3) {
			t.Fatal("quarantine threshold", i, revoked, e)
		}
	}
	if e = r.Heartbeat(ctx, "worker", session); !errors.Is(e, ErrStale) {
		t.Fatal("quarantine heartbeat accepted", e)
	}
	if _, e = r.Lease(ctx, epoch, "worker", session, "after-quarantine"); !errors.Is(e, ErrStale) {
		t.Fatal("quarantine lease accepted", e)
	}
	_ = provisionTestWorker(t, r, "other")
	if _, e = r.DB.Exec(ctx, `UPDATE compute_worker SET invalid_since=clock_timestamp()-interval '11 minutes',invalid_count=2 WHERE id='other'`); e != nil {
		t.Fatal(e)
	}
	if e = r.InvalidResult(ctx, "other"); e != nil {
		t.Fatal(e)
	}
	var revoked bool
	var count int
	if e = r.DB.QueryRow(ctx, `SELECT revoked,invalid_count FROM compute_worker WHERE id='other'`).Scan(&revoked, &count); e != nil || revoked || count != 1 {
		t.Fatal("invalid window did not restart", count, revoked, e)
	}
	var audited int
	if e = r.DB.QueryRow(ctx, `SELECT count(*) FROM compute_audit WHERE action='invalid_result'`).Scan(&audited); e != nil || audited != 4 {
		t.Fatal("missing durable audit", audited, e)
	}
}
