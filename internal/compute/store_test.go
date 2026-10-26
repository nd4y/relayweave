package compute

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"proxyplatform/internal/identity"
)

func TestComputeZeroOneTwoWorkerProtocol(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			r := testRepository(t)
			// Provisioning/fsync is out of band, before the online deadline.
			setup := context.Background()
			dir := t.TempDir()
			if e := Bootstrap(dir); e != nil {
				t.Fatal(e)
			}
			serverTLS, e := identity.ServerTLS(dir)
			if e != nil {
				t.Fatal(e)
			}
			seed, e := ReadKey(filepath.Join(dir, "signing.seed"))
			if e != nil {
				t.Fatal(e)
			}
			controller := &Controller{Repo: r, Key: ed25519.NewKeyFromSeed(seed), KeyID: "key"}
			server := httptest.NewUnstartedServer(controller)
			server.TLS = serverTLS
			server.StartTLS()
			defer server.Close()
			clients := []*Client{}
			for i := 0; i < count; i++ {
				peer := t.TempDir()
				id := fmt.Sprintf("compute-%d", i)
				if e = Provision(setup, r, dir, peer, id); e != nil {
					t.Fatal(e)
				}
				config, err := identity.ClientTLS(peer)
				if err != nil {
					t.Fatal(err)
				}
				config.ServerName = "compute-dispatcher"
				client, err := NewClient(server.URL, config)
				if err != nil {
					t.Fatal(err)
				}
				clients = append(clients, client)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			epoch, e := r.Acquire(ctx)
			if e != nil {
				t.Fatal(e)
			}
			controller.Epoch.Store(epoch)
			var wg sync.WaitGroup
			for _, client := range clients {
				wg.Add(1)
				go func(client *Client) {
					defer wg.Done()
					_ = client.Run(ctx, func(ctx context.Context, task Task) (Result, error) {
						if binary := os.Getenv("RW_COMPUTE_WORKER_BINARY"); binary != "" {
							return executeBinary(ctx, binary, task)
						}
						candidate, e := Search(ctx, task.Snapshot, task.First, task.Count)
						status := "ok"
						if candidate.ID == 0 {
							status = "no_improvement"
						}
						return Result{Version: 1, OwnerEpoch: task.OwnerEpoch, Task: task.ID, Attempt: task.Attempt, Session: task.Session, SnapshotDigest: task.SnapshotDigest, Status: status, Candidate: candidate, CPUUsedMS: 1}, e
					})
				}(client)
			}
			defer func() { cancel(); wg.Wait() }()
			for count > 0 {
				var ready int
				if e = r.DB.QueryRow(ctx, `SELECT count(*) FROM compute_worker WHERE session<>''`).Scan(&ready); e != nil {
					t.Fatal(e)
				}
				if ready == count {
					break
				}
				if !sleep(ctx, 20*time.Millisecond) {
					t.Fatal("workers not registered")
				}
			}
			s := planFixture()
			s.Installation = r.Installation
			if e = controller.Offer(ctx, s); e != nil {
				t.Fatal(e)
			}
			if count == 0 {
				var jobs int
				if e = r.DB.QueryRow(ctx, `SELECT count(*) FROM compute_task`).Scan(&jobs); e != nil || jobs != 0 {
					t.Fatal("issued without workers", jobs, e)
				}
				return
			}
			for {
				published, err := controller.Latest(ctx, s.Executor)
				if err != nil {
					t.Fatal(err)
				}
				if len(published) > 0 {
					if err = CheckSignature(published[0], controller.Key.Public().(ed25519.PublicKey)); err != nil {
						t.Fatal(err)
					}
					break
				}
				if !sleep(ctx, 20*time.Millisecond) {
					t.Fatal("no verified result from worker protocol")
				}
			}
		})
	}
}

func testRepository(t *testing.T) *Repository {
	t.Helper()
	url := os.Getenv("RW_COMPUTE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("isolated build-01 PG integration fixture required")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	schema := "compute_test_" + newID()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		t.Fatal(e)
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "1000"
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "1000"
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		db.Close()
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	r := &Repository{DB: db, Installation: "test-installation", Owner: newID(), TokenKey: make([]byte, 32)}
	if e = r.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = r.Migrate(ctx); e != nil {
		t.Fatal("migration not idempotent", e)
	}
	return r
}

func provisionTestWorker(t *testing.T, r *Repository, id string) string {
	t.Helper()
	if e := r.ApproveCertificate(context.Background(), id, tokenHash(id)); e != nil {
		t.Fatal(e)
	}
	s, e := r.Register(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestRepositoryFencingBudgetIdempotencyAndOutbox(t *testing.T) {
	r := testRepository(t)
	ctx := context.Background()
	epoch, e := r.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	sessions := []string{provisionTestWorker(t, r, "worker-a"), provisionTestWorker(t, r, "worker-b")}
	snapshot := planFixture()
	snapshot.Installation = r.Installation
	if e = r.Offer(ctx, epoch, snapshot); e != nil {
		t.Fatal(e)
	}
	var tasks [2]Task
	var errs [2]error
	var wg sync.WaitGroup
	for i, id := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			tasks[i], errs[i] = r.Lease(ctx, epoch, id, sessions[i], "request-"+id)
		}(i, id)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil || tasks[i].Validate() != nil {
			t.Fatal("lease", i, e, tasks[i].Validate())
		}
	}
	if tasks[0].ID == tasks[1].ID {
		t.Fatal("task assigned twice")
	}
	again, e := r.Lease(ctx, epoch, "worker-a", sessions[0], "request-worker-a")
	if e != nil || again.Attempt != tasks[0].Attempt || again.Token != tasks[0].Token {
		t.Fatal("lost response created new attempt", e)
	}
	var stored []byte
	if e = r.DB.QueryRow(ctx, `SELECT payload FROM compute_attempt WHERE id=$1`, again.Attempt).Scan(&stored); e != nil {
		t.Fatal(e)
	}
	var persisted Task
	_ = StrictDecode(stored, &persisted)
	if persisted.Token != "" {
		t.Fatal("plaintext capability persisted")
	}
	for i, task := range tasks {
		candidate, err := Search(ctx, task.Snapshot, task.First, task.Count)
		if err != nil {
			t.Fatal(err)
		}
		status := "ok"
		if candidate.ID == 0 {
			status = "no_improvement"
		}
		result := Result{Version: 1, OwnerEpoch: epoch, Task: task.ID, Attempt: task.Attempt, Session: task.Session, SnapshotDigest: task.SnapshotDigest, Status: status, Candidate: candidate, CPUUsedMS: 1}
		id := []string{"worker-a", "worker-b"}[i]
		if _, e = r.Accept(ctx, epoch, id, "wrong", result); !errors.Is(e, ErrStale) {
			t.Fatal("bad capability accepted", e)
		}
		if _, e = r.Accept(ctx, epoch, id, task.Token, result); e != nil {
			t.Fatal(e)
		}
		if _, e = r.Accept(ctx, epoch, id, task.Token, result); e != nil {
			t.Fatal("duplicate receipt", e)
		}
		result.CPUUsedMS++
		if _, e = r.Accept(ctx, epoch, id, task.Token, result); !errors.Is(e, ErrConflict) {
			t.Fatal("conflicting receipt", e)
		}
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	controller := &Controller{Repo: r, Key: key, KeyID: "signing-key"}
	controller.Epoch.Store(epoch)
	if e = controller.publishPending(ctx); e != nil {
		t.Fatal(e)
	}
	published, e := controller.Latest(ctx, snapshot.Executor)
	if e != nil || len(published) != 1 {
		t.Fatal("outbox did not recover", len(published), e)
	}
	if e = CheckSignature(published[0], key.Public().(ed25519.PublicKey)); e != nil {
		t.Fatal(e)
	}
	snapshot.Sequence++
	if e = r.Offer(ctx, epoch, snapshot); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Lease(ctx, epoch, "worker-a", sessions[0], "new-request"); !errors.Is(e, ErrBudget) {
		t.Fatal("rolling CPU reservation exceeded", e)
	}
	if _, e = r.DB.Exec(ctx, `UPDATE compute_installation_state SET lease_until=clock_timestamp()-interval '1 second'`); e != nil {
		t.Fatal(e)
	}
	other := *r
	other.Owner = newID()
	next, e := other.Acquire(ctx)
	if e != nil || next <= epoch {
		t.Fatal("takeover", e)
	}
	if e = r.Offer(ctx, epoch, snapshot); !errors.Is(e, ErrStale) {
		t.Fatal("stale owner wrote", e)
	}
	if e = other.Uncertain(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = other.Acquire(ctx); !errors.Is(e, ErrStale) {
		t.Fatal("uncertain budget issued", e)
	}
	if e = other.Cleanup(ctx); e != nil {
		t.Fatal("retention query", e)
	}
}

func TestRepositoryDailyBudgetClockRollbackAndRevocation(t *testing.T) {
	r := testRepository(t)
	ctx := context.Background()
	epoch, e := r.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	session := provisionTestWorker(t, r, "worker")
	s := planFixture()
	s.Installation = r.Installation
	if e = r.Offer(ctx, epoch, s); e != nil {
		t.Fatal(e)
	}
	if _, e = r.DB.Exec(ctx, `INSERT INTO compute_budget_window(installation,window_id,cpu_ms)VALUES($1,'day/'||to_char(clock_timestamp()AT TIME ZONE'UTC','YYYY-MM-DD'),10800000)`, r.Installation); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Lease(ctx, epoch, "worker", session, "daily"); !errors.Is(e, ErrDailyBudget) {
		t.Fatal("daily cap bypass", e)
	}
	if _, e = r.DB.Exec(ctx, `DELETE FROM compute_budget_window`); e != nil {
		t.Fatal(e)
	}
	if _, e = r.DB.Exec(ctx, `UPDATE compute_installation_state SET high_day=(clock_timestamp()AT TIME ZONE'UTC')::date+1`); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Lease(ctx, epoch, "worker", session, "clock-rollback"); !errors.Is(e, ErrBudget) {
		t.Fatal("clock rollback replenished budget", e)
	}
	if e = r.Revoke(ctx, "worker"); e != nil {
		t.Fatal(e)
	}
	if e = r.Heartbeat(ctx, "worker", session); !errors.Is(e, ErrStale) {
		t.Fatal("revocation ignored", e)
	}
}

func TestComputeMTLSScopeRenewalAndRevocation(t *testing.T) {
	r := testRepository(t)
	ctx := context.Background()
	dir := t.TempDir()
	if e := Bootstrap(dir); e != nil {
		t.Fatal(e)
	}
	if e := Bootstrap(dir); !errors.Is(e, ErrConflict) {
		t.Fatal("bootstrap overwrote authority", e)
	}
	workerDir := t.TempDir()
	if e := Provision(ctx, r, dir, workerDir, "compute-peer"); e != nil {
		t.Fatal(e)
	}
	serverTLS, e := identity.ServerTLS(dir)
	if e != nil {
		t.Fatal(e)
	}
	seed, e := ReadKey(filepath.Join(dir, "signing.seed"))
	if e != nil {
		t.Fatal(e)
	}
	a, e := computeAuthority(dir)
	if e != nil {
		t.Fatal(e)
	}
	controller := &Controller{Repo: r, Key: ed25519.NewKeyFromSeed(seed), KeyID: "test-key", Renew: func(id string, csr []byte) ([]byte, error) {
		bundle, e := a.IssueCompute(id, false, csr)
		return bundle.Certificate, e
	}}
	epoch, e := r.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	controller.Epoch.Store(epoch)
	server := httptest.NewUnstartedServer(controller)
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()
	clientTLS, e := identity.ClientTLS(workerDir)
	if e != nil {
		t.Fatal(e)
	}
	clientTLS.ServerName = "compute-dispatcher"
	client, e := NewClient(server.URL, clientTLS)
	if e != nil {
		t.Fatal(e)
	}
	defer client.HTTP.CloseIdleConnections()
	// Force the same pinned key into the renewal window without changing the
	// authority trust anchor. The renewal then has to preserve its private key.
	oldCertificate, e := identity.Load(filepath.Join(workerDir, "client.json"))
	if e != nil {
		t.Fatal(e)
	}
	csr, e := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, oldCertificate.PrivateKey)
	if e != nil {
		t.Fatal(e)
	}
	caExpiry := a.Cert.NotAfter
	a.Cert.NotAfter = time.Now().Add(24 * time.Hour)
	short, e := a.IssueCompute("compute-peer", false, csr)
	a.Cert.NotAfter = caExpiry
	if e != nil {
		t.Fatal(e)
	}
	oldRaw, e := os.ReadFile(filepath.Join(workerDir, "client.json"))
	if e != nil {
		t.Fatal(e)
	}
	var bundle identity.Bundle
	if json.Unmarshal(oldRaw, &bundle) != nil {
		t.Fatal("bundle")
	}
	bundle.Certificate = short.Certificate
	if e = identity.Save(filepath.Join(workerDir, "client.json"), bundle); e != nil {
		t.Fatal(e)
	}
	if e = client.RenewIdentity(ctx, workerDir); e != nil {
		t.Fatal("renewal", e)
	}
	renewed, e := identity.Load(filepath.Join(workerDir, "client.json"))
	if e != nil {
		t.Fatal(e)
	}
	renewedLeaf, e := x509.ParseCertificate(renewed.Certificate[0])
	if e != nil || time.Until(renewedLeaf.NotAfter) < 6*24*time.Hour {
		t.Fatal("short certificate not renewed", e)
	}
	var reply registerResponse
	if code, e := client.post(ctx, "/compute/v1/register", "", sessionRequest{Version: 1}, &reply); e != nil || code != 200 || reply.Session == "" {
		t.Fatal("mTLS registration", code, e)
	}
	// A data-plane leaf from a different CA cannot reach the HTTP handler.
	wrongDir := t.TempDir()
	wrong, e := identity.Open(wrongDir)
	if e != nil {
		t.Fatal(e)
	}
	leaf, e := wrong.Issue("compute-peer", false, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = identity.Save(filepath.Join(wrongDir, "client.json"), leaf); e != nil {
		t.Fatal(e)
	}
	certificate, e := identity.Load(filepath.Join(wrongDir, "client.json"))
	if e != nil {
		t.Fatal(e)
	}
	badTLS := clientTLS.Clone()
	badTLS.GetClientCertificate = nil
	badTLS.Certificates = []tls.Certificate{certificate}
	bad, _ := NewClient(server.URL, badTLS)
	defer bad.HTTP.CloseIdleConnections()
	if _, e = bad.post(ctx, "/compute/v1/register", "", sessionRequest{Version: 1}, nil); e == nil {
		t.Fatal("wrong CA accepted")
	}
	if e = r.Revoke(ctx, "compute-peer"); e != nil {
		t.Fatal(e)
	}
	if code, _ := client.post(ctx, "/compute/v1/heartbeat", "", sessionRequest{Version: 1, Session: reply.Session}, nil); code != 403 {
		t.Fatal("reused TLS connection bypassed revoke", code)
	}
}

func TestEvaluateChildBounded(t *testing.T) {
	if os.Getenv("RW_COMPUTE_WORKER_BINARY") == "" {
		t.Skip("build-01 compiled child fixture required")
	}
	// ExecuteChild uses the running test binary, so the separate process is
	// invoked directly here with the identical evaluate protocol.
	s := planFixture()
	h, _ := Hash(s)
	task := Task{Version: 1, OwnerEpoch: 1, ID: "task", Attempt: "attempt", Session: "session", Token: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Snapshot: s, SnapshotDigest: h, First: 0, Count: 64, CPUReservationMS: 500, WallBudgetMS: 5000}
	for range 10 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		result, e := executeBinary(ctx, os.Getenv("RW_COMPUTE_WORKER_BINARY"), task)
		cancel()
		if e != nil || result.CPUUsedMS > 500 || result.Candidate.ID == 0 {
			t.Fatal("child", e, result.CPUUsedMS)
		}
	}
}
