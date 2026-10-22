package compute

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Exercise the authenticated HTTP boundary and durable PG accounting. Actual
// TLS verification/reused-connection revocation has a separate integration test.
func invalidResultPeer(t *testing.T, repo *Repository, id string) (*Controller, *tls.ConnectionState) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.ApproveCertificate(context.Background(), id, tokenHash(string(spki))); err != nil {
		t.Fatal(err)
	}
	epoch, err := repo.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	controller := &Controller{Repo: repo}
	controller.Epoch.Store(epoch)
	peer := &x509.Certificate{Subject: pkix.Name{CommonName: id}, PublicKey: public,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	return controller, &tls.ConnectionState{PeerCertificates: []*x509.Certificate{peer}, VerifiedChains: [][]*x509.Certificate{{peer}}}
}

func invalidResultState(t *testing.T, repo *Repository, id string) (int, bool, int) {
	t.Helper()
	var count, audits int
	var revoked bool
	err := repo.DB.QueryRow(context.Background(), `SELECT invalid_count,revoked,
 (SELECT count(*) FROM compute_audit WHERE installation=$1 AND subject=$2 AND action='invalid_result')
 FROM compute_worker WHERE installation=$1 AND id=$2`, repo.Installation, id).Scan(&count, &revoked, &audits)
	if err != nil {
		t.Fatal(err)
	}
	return count, revoked, audits
}

func TestInvalidResultHTTPQuarantine(t *testing.T) {
	repo := testRepository(t)
	cases := []struct {
		name, body, encoding, authorization string
		code                                int
	}{
		{"syntax", `{`, "", "Bearer token", 400},
		{"duplicate", `{"version":1,"version":1}`, "", "Bearer token", 400},
		{"unknown", `{"unsupported":1}`, "", "Bearer token", 400},
		{"fractional", `{"version":1.5}`, "", "Bearer token", 400},
		{"oversize", strings.Repeat(" ", (32<<10)+1), "", "Bearer token", 413},
		{"compression", `{}`, "gzip", "Bearer token", 400},
		{"missing_capability", `{}`, "", "", 400},
		{"typed_invalid", `{}`, "", "Bearer token", 400},
	}
	for _, path := range []string{"/compute/v1/result", "/compute/v1/cancelled"} {
		for _, tc := range cases {
			t.Run(strings.TrimPrefix(path, "/compute/v1/")+"/"+tc.name, func(t *testing.T) {
				id := newID()
				controller, secure := invalidResultPeer(t, repo, id)
				for attempt := 1; attempt <= 3; attempt++ {
					request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(tc.body))
					request.TLS = secure
					request.Header.Set("Authorization", tc.authorization)
					if tc.encoding != "" {
						request.Header.Set("Content-Encoding", tc.encoding)
					}
					response := httptest.NewRecorder()
					controller.ServeHTTP(response, request)
					if response.Code != tc.code {
						t.Fatalf("attempt %d: HTTP %d, want %d", attempt, response.Code, tc.code)
					}
					count, revoked, audits := invalidResultState(t, repo, id)
					if count != attempt || audits != attempt || revoked != (attempt == 3) {
						t.Fatalf("attempt %d: count=%d audits=%d revoked=%v", attempt, count, audits, revoked)
					}
				}
				// Existing authenticated connections cannot obtain another lease or
				// register after the third rejection. Check before request rate expiry.
				request := httptest.NewRequest(http.MethodPost, "/compute/v1/register", strings.NewReader(`{"version":1}`))
				request.TLS = secure
				response := httptest.NewRecorder()
				controller.ServeHTTP(response, request)
				if response.Code != http.StatusForbidden {
					t.Fatalf("quarantined peer registered: HTTP %d", response.Code)
				}
			})
		}
	}
}

type interruptedResultBody struct {
	reader io.Reader
	cancel context.CancelFunc
	err    error
}

func (b *interruptedResultBody) Read(p []byte) (int, error) {
	if b.cancel != nil {
		b.cancel()
	}
	if b.err != nil {
		return 0, b.err
	}
	return b.reader.Read(p)
}
func (*interruptedResultBody) Close() error { return nil }

func TestInvalidResultHTTPAccountingBoundaries(t *testing.T) {
	repo := testRepository(t)
	for _, tc := range []string{"unapproved_key", "partial_upload", "unrelated_endpoint", "stale_attempt", "disconnect", "expired_window", "audit_failure"} {
		t.Run(tc, func(t *testing.T) {
			id := newID()
			controller, secure := invalidResultPeer(t, repo, id)
			request := httptest.NewRequest(http.MethodPost, "/compute/v1/result", bytes.NewBufferString(`{`))
			request.TLS = secure
			wantCode, wantCount, wantAudits := 400, 1, 1
			switch tc {
			case "unapproved_key":
				if _, err := repo.DB.Exec(context.Background(), `UPDATE compute_worker SET key_hash='' WHERE installation=$1 AND id=$2`, repo.Installation, id); err != nil {
					t.Fatal(err)
				}
				wantCode, wantCount, wantAudits = 403, 0, 0
			case "partial_upload":
				request.Body = &interruptedResultBody{err: io.ErrUnexpectedEOF}
				wantCode, wantCount, wantAudits = 413, 0, 0
			case "unrelated_endpoint":
				request.URL.Path = "/compute/v1/heartbeat"
				wantCount, wantAudits = 0, 0
			case "stale_attempt":
				raw, err := Canonical(Result{Version: 1, OwnerEpoch: controller.Epoch.Load(), Attempt: "absent", Task: "absent", Session: "session", SnapshotDigest: strings.Repeat("0", 64), Status: "no_improvement"})
				if err != nil {
					t.Fatal(err)
				}
				request.Body = io.NopCloser(bytes.NewReader(raw))
				request.Header.Set("Authorization", "Bearer token")
				wantCode, wantCount, wantAudits = 410, 0, 0
			case "disconnect":
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				request = request.WithContext(ctx)
				request.Body = &interruptedResultBody{reader: strings.NewReader(`{`), cancel: cancel}
			case "expired_window":
				if _, err := repo.DB.Exec(context.Background(), `UPDATE compute_worker SET invalid_count=2,invalid_since=clock_timestamp()-interval '11 minutes' WHERE installation=$1 AND id=$2`, repo.Installation, id); err != nil {
					t.Fatal(err)
				}
			case "audit_failure":
				// Audit and counter must roll back together. A protocol-level 400
				// must not hide the failure to persist the quarantine decision.
				if _, err := repo.DB.Exec(context.Background(), `ALTER TABLE compute_audit ADD CONSTRAINT reject_invalid_result CHECK(action<>'invalid_result') NOT VALID`); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := repo.DB.Exec(context.Background(), `ALTER TABLE compute_audit DROP CONSTRAINT reject_invalid_result`); err != nil {
						t.Error(err)
					}
				}()
				wantCode, wantCount, wantAudits = 503, 0, 0
			}
			response := httptest.NewRecorder()
			controller.ServeHTTP(response, request)
			if response.Code != wantCode {
				t.Fatalf("HTTP %d, want %d", response.Code, wantCode)
			}
			count, revoked, audits := invalidResultState(t, repo, id)
			if count != wantCount || audits != wantAudits || revoked {
				t.Fatalf("count=%d audits=%d revoked=%v", count, audits, revoked)
			}
		})
	}
	var publications int
	if err := repo.DB.QueryRow(context.Background(), `SELECT count(*) FROM compute_publication`).Scan(&publications); err != nil || publications != 0 {
		t.Fatal("invalid input published a recommendation", publications, err)
	}
}
