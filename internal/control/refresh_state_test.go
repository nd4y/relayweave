package control

import (
	"proxyplatform/internal/catalog"
	"testing"
	"time"
)

func TestPendingRefreshRefetchesWithoutReplayingApprovalOrRenewingHold(t *testing.T) {
	old := time.Now().UTC().Add(-time.Hour)
	source := catalog.Source{PendingSnapshot: "legacy-pending", ETag: "stable-etag", LastModified: "stable-date", LastSuccess: old, LastError: "review_required", MaxStaleSeconds: 7200, Headers: map[string]string{"if-none-match": "header-etag", "IF-MODIFIED-SINCE": "header-date", "X-Provider": "synthetic"}}
	fetch := sourceForRefresh(source, "refresh")
	if fetch.ETag != "" || fetch.LastModified != "" || len(fetch.Headers) != 1 || fetch.Headers["X-Provider"] != "synthetic" || len(source.Headers) != 3 || source.ETag != "stable-etag" {
		t.Fatal("pending refresh remained conditional or mutated stored source")
	}
	now := time.Now().UTC()
	result := sourceNotModified(source, map[string]int64{"total": 100}, now)
	if !result.LastSuccess.Equal(old) || result.PendingSnapshot != source.PendingSnapshot || result.LastError != source.LastError || !catalog.SourceUntil(result).Equal(catalog.SourceUntil(source)) || !result.LastFetched.Equal(now) {
		t.Fatal("304 changed pending trust or renewed held authorization")
	}
	source.PendingSnapshot = ""
	if sourceForRefresh(source, "refresh").ETag != "stable-etag" || !sourceNotModified(source, nil, now).LastSuccess.Equal(now) {
		t.Fatal("normal accepted 304 stopped renewing the accepted catalogue")
	}
}
