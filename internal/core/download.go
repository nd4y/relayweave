package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type artifactHTTPError struct{ status int }

func (e *artifactHTTPError) Error() string { return fmt.Sprintf("artifact HTTP status %d", e.status) }

// ArtifactFailureCode exports only fixed categories. Network errors can include
// URLs, redirect tokens or local paths and must never be logged verbatim.
func ArtifactFailureCode(err error) string {
	var dns *net.DNSError
	var network net.Error
	var status *artifactHTTPError
	switch {
	case errors.As(err, &dns):
		return "artifact_dns_error"
	case errors.Is(err, context.Canceled):
		return "artifact_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "artifact_timeout"
	case errors.As(err, &network) && network.Timeout():
		return "artifact_timeout"
	case errors.As(err, &status):
		if status.status == 429 || status.status >= 500 {
			return "artifact_http_unavailable"
		}
		return "artifact_http_rejected"
	case errors.Is(err, syscall.ENOSPC):
		return "artifact_storage_full"
	case os.IsPermission(err):
		return "artifact_storage_denied"
	case strings.Contains(err.Error(), "SHA-256 mismatch"), strings.Contains(err.Error(), "failed verification"):
		return "artifact_integrity_error"
	default:
		return "artifact_preparation_error"
	}
}

// Retry only an idempotent, approved GET before its response body is consumed.
// The caller's deadline bounds the entire operation. Integrity, archive and
// executable checks remain outside this loop and are never retried here.
func artifactResponse(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		response, err := client.Do(req.Clone(ctx))
		if err != nil && response != nil {
			response.Body.Close()
		}
		if ctx.Err() != nil {
			if err == nil && response != nil {
				response.Body.Close()
			}
			return nil, ctx.Err()
		}
		retry := false
		failure := err
		if err != nil {
			var network net.Error
			retry = (errors.As(err, &network) && (network.Timeout() || network.Temporary())) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)
		} else {
			retry = response.StatusCode == 429 || response.StatusCode >= 500
			failure = &artifactHTTPError{status: response.StatusCode}
		}
		if !retry || attempt == 2 {
			return response, err
		}
		delay := time.Second << attempt
		if response != nil {
			if value := response.Header.Get("Retry-After"); value != "" {
				var requested time.Duration
				if seconds, parseErr := strconv.Atoi(value); parseErr == nil && seconds > 0 {
					if seconds > 30 {
						return response, nil
					}
					requested = time.Duration(seconds) * time.Second
				} else if when, parseErr := http.ParseTime(value); parseErr == nil {
					requested = time.Until(when)
				}
				// Leave long backoff to a later explicit rollout; don't hammer a
				// rate-limited provider or hold this apply indefinitely.
				if requested > 30*time.Second {
					return response, nil
				}
				delay = max(delay, requested)
			}
			response.Body.Close()
		}
		slog.Warn("artifact download retry", "attempt", attempt+2, "reason", ArtifactFailureCode(failure))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
