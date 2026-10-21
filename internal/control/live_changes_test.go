package control

import (
	"encoding/json"
	"testing"
	"time"

	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
)

func TestSourceFetchEditsDoNotBlockFreshRuntimeReconciliation(t *testing.T) {
	baseline := catalog.Source{
		URL: "https://provider.example/subscription?token=old", Headers: map[string]string{"X-Device": "old"},
		Enabled: true, CoreVersion: "26.10.17", IntervalSeconds: 3600, MaxStaleSeconds: 300,
		LastSuccess: time.Unix(1000, 0).UTC(), Quota: map[string]int64{"total": 1000},
	}
	data, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	previous := store.Object{Kind: "sources", ID: "source", Name: "source", Data: data}
	for _, test := range []struct {
		name   string
		change func(*catalog.Source)
		want   bool
	}{
		{"unchanged", func(*catalog.Source) {}, false},
		{"URL token", func(s *catalog.Source) { s.URL = "https://provider.example/subscription?token=new" }, false},
		{"device headers", func(s *catalog.Source) { s.Headers["X-Device"] = "new" }, false},
		{"refresh schedule", func(s *catalog.Source) { s.IntervalSeconds = 60 }, false},
		{"fetch destination permission", func(s *catalog.Source) { s.AllowPrivate = true }, false},
		{"fetch metadata", func(s *catalog.Source) {
			s.ETag = "new"
			s.LastModified = "new"
			s.NextRefresh = time.Unix(2000, 0).UTC()
			s.LastError = "source_fetch_failed"
			s.PendingSnapshot = "unapproved-snapshot"
		}, false},
		{"revoke source", func(s *catalog.Source) { s.Enabled = false }, true},
		{"engine selection", func(s *catalog.Source) { s.CoreVersion = "26.9.30" }, true},
		{"expiry budget", func(s *catalog.Source) { s.MaxStaleSeconds = 60 }, true},
		{"profile filter", func(s *catalog.Source) { s.Filter.UDPOnly = true }, true},
		{"successful freshness", func(s *catalog.Source) { s.LastSuccess = time.Unix(2000, 0).UTC() }, true},
		{"provider quota", func(s *catalog.Source) { s.Quota["total"] = 2000 }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			next, err := catalog.Decode[catalog.Source](previous.Data)
			if err != nil {
				t.Fatal(err)
			}
			test.change(&next)
			updated := previous
			updated.Data, err = json.Marshal(next)
			if err != nil {
				t.Fatal(err)
			}
			if got := sourceRuntimeChanged(previous, updated); got != test.want {
				t.Fatalf("runtime reconciliation required=%t, want=%t", got, test.want)
			}
		})
	}
	updated := previous
	updated.Name = "renamed source"
	if !sourceRuntimeChanged(previous, updated) {
		t.Fatal("source-name filters were not reconciled")
	}
	updated = previous
	updated.Data = json.RawMessage(`{`)
	if !sourceRuntimeChanged(previous, updated) {
		t.Fatal("malformed state bypassed reconciliation")
	}
}
