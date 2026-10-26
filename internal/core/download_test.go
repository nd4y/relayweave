package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

type artifactRoundTrip func(*http.Request) (*http.Response, error)

func (f artifactRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type artifactTestBody struct {
	io.Reader
	closed bool
}

func (b *artifactTestBody) Close() error { b.closed = true; return nil }

func TestArtifactDownloadRetriesTemporaryFailuresAndClosesFailedResponse(t *testing.T) {
	calls := 0
	failedBody := &artifactTestBody{Reader: strings.NewReader("temporary failure")}
	client := &http.Client{Transport: artifactRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return nil, &net.DNSError{Err: "server misbehaving", Name: "private-name.invalid", IsTemporary: true}
		case 2:
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: failedBody}, nil
		default:
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("verified later"))}, nil
		}
	})}
	request, _ := http.NewRequest(http.MethodGet, "https://example.invalid/private-download-token", nil)
	response, err := artifactResponse(t.Context(), client, request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if calls != 3 || response.StatusCode != 200 || !failedBody.closed {
		t.Fatal("temporary download failure did not recover with closed discarded body")
	}
}

func TestArtifactDownloadRetryBudgetAndPermanentRejections(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		status     int
		retryAfter string
		attempts   int
	}{
		{"temporary budget", 503, "", 3}, {"permanent rejection", 403, "", 1}, {"long rate limit", 429, "120", 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: artifactRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: scenario.status, Header: http.Header{"Retry-After": []string{scenario.retryAfter}}, Body: io.NopCloser(strings.NewReader("failure"))}, nil
			})}
			request, _ := http.NewRequest(http.MethodGet, "https://example.invalid/download", nil)
			response, err := artifactResponse(t.Context(), client, request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if calls != scenario.attempts || response.StatusCode != scenario.status {
				t.Fatalf("unexpected attempts/status: %d/%d", calls, response.StatusCode)
			}
		})
	}
}

func TestArtifactDownloadCancellationInterruptsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	client := &http.Client{Transport: artifactRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		time.AfterFunc(25*time.Millisecond, cancel)
		return nil, &net.DNSError{Err: "temporary", IsTemporary: true}
	})}
	request, _ := http.NewRequest(http.MethodGet, "https://example.invalid/download", nil)
	response, err := artifactResponse(ctx, client, request)
	if response != nil || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancellation retried or escaped: %d, %v", calls, err)
	}
}

func TestArtifactFailureCodesDoNotExposeNetworkURLsOrPaths(t *testing.T) {
	for _, sample := range []struct {
		err  error
		code string
	}{
		{&url.Error{Op: "Get", URL: "https://private.invalid/secret?token=private", Err: &net.DNSError{Name: "private.invalid", Err: "server misbehaving"}}, "artifact_dns_error"},
		{&os.PathError{Op: "write", Path: "/private/secret", Err: syscall.ENOSPC}, "artifact_storage_full"},
		{&artifactHTTPError{status: 503}, "artifact_http_unavailable"},
		{&artifactHTTPError{status: 403}, "artifact_http_rejected"},
		{errors.New("archive SHA-256 mismatch"), "artifact_integrity_error"},
		{errors.New("private arbitrary failure"), "artifact_preparation_error"},
	} {
		if actual := ArtifactFailureCode(sample.err); actual != sample.code {
			t.Fatalf("unexpected fixed category: %s", actual)
		}
	}
}
