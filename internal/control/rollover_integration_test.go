package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
	"testing"
	"time"
)

// An isolated disposable database is required. No existing application data is
// deleted: each run creates its own source and inspects only that source.
func TestSubscriptionSnapshotTransactions(t *testing.T) {
	dsn := os.Getenv("ROLLOVER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.New(ctx, dsn, filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	s := &Server{Store: db, ctx: ctx, changed: make(chan struct{}), jobs: make(chan struct{}, 1)}
	source := catalog.Source{URL: "https://fixture.invalid/subscription", Enabled: true, UpdatePolicy: "auto_safe", MaxStaleSeconds: 3600}
	data, _ := json.Marshal(source)
	o, err := db.Put(ctx, store.Object{Kind: "sources", Name: "Synthetic rollover", Data: data}, 0)
	if err != nil {
		t.Fatal(err)
	}
	parse := func(host, uuid string) catalog.Parsed {
		p, e := catalog.Parse([]byte("vless://" + uuid + "@" + host + ":443?security=tls&sni=peer.example"))
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	credential := "11111111-1111-1111-1111-111111111111"
	initial := parse("203.0.113.10", credential)
	if err = s.commitSnapshot(ctx, o, source, sourceSnapshot{SourceID: o.ID, Fetched: time.Now().UTC(), Parsed: initial}); err != nil {
		t.Fatal(err)
	}
	items, _, err := db.List(ctx, "profiles", 20000, 0)
	if err != nil {
		t.Fatal(err)
	}
	var old store.Object
	for _, item := range items {
		p, _ := catalog.Decode[catalog.Profile](item.Data)
		if p.SourceID == o.ID {
			old = item
			break
		}
	}
	if old.ID == "" {
		t.Fatal("snapshot not committed")
	}
	o, err = db.Get(ctx, "sources", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, _ = catalog.Decode[catalog.Source](o.Data)
	frozen := catalog.SourceUntil(source)
	next := parse("203.0.113.20", credential)
	diff, err := s.sourceDiff(ctx, o.ID, next.Profiles)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotReview(diff) || diff["address_rotations"] != 1 {
		t.Fatal("safe rotation held", diff)
	}
	if err = s.commitSnapshot(ctx, o, source, sourceSnapshot{SourceID: o.ID, Fetched: time.Now().UTC(), Parsed: next}); err != nil {
		t.Fatal(err)
	}
	current, err := db.Get(ctx, "profiles", old.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := catalog.Decode[catalog.Profile](current.Data)
	if p.Host != "203.0.113.20" || len(p.Previous) != 1 || !p.Previous[0].ValidUntil.Equal(frozen) {
		t.Fatal("identity/authorization overlap not atomic")
	}
	stale := sourceSnapshot{SourceID: o.ID, Fetched: time.Now().UTC(), Parsed: initial}
	if err = s.commitSnapshot(ctx, o, source, stale); err != store.ErrConflict {
		t.Fatal("stale source CAS committed", err)
	}
	current, _ = db.Get(ctx, "profiles", old.ID)
	p, _ = catalog.Decode[catalog.Profile](current.Data)
	if p.Host != "203.0.113.20" {
		t.Fatal("failed transaction changed derived catalogue")
	}
	risky := parse("203.0.113.20", "22222222-2222-2222-2222-222222222222")
	diff, err = s.sourceDiff(ctx, o.ID, risky.Profiles)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotReview(diff) {
		t.Fatal("changed credential accepted as address rotation")
	}
	merged, held, changed := safePartial([]catalog.Profile{p}, risky.Profiles, nil)
	if changed || len(held) != 1 || merged[0].Fingerprint != p.Fingerprint {
		t.Fatal("risky partial altered LKG")
	}
}
