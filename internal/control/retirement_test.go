package control

import (
	"encoding/json"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
	"testing"
	"time"
)

func TestSourceRemovalHasFrozenContinuationGrant(t *testing.T) {
	now := time.Now()
	until := now.Add(time.Minute)
	source := catalog.Source{Enabled: true, LastSuccess: now, MaxStaleSeconds: 3600}
	data, _ := json.Marshal(source)
	p := catalog.Profile{SourceID: "s", Present: false, Enabled: true, Supported: true, Fingerprint: "11111111111111111111111111111111", RetiredUntil: until}
	permits, err := livePermits(now, []store.Object{{ID: "s", Data: data}}, map[string]catalog.Profile{"p": p})
	if err != nil || !permits["p"].EstablishedOnly || !permits["p"].ValidUntil.Equal(until) {
		t.Fatal("retirement grant", err, permits)
	}
	permits, err = livePermits(now.Add(2*time.Minute), []store.Object{{ID: "s", Data: data}}, map[string]catalog.Profile{"p": p})
	if err != nil || len(permits) != 0 {
		t.Fatal("retirement authorization renewed")
	}
	source.Enabled = false
	data, _ = json.Marshal(source)
	permits, _ = livePermits(now, []store.Object{{ID: "s", Data: data}}, map[string]catalog.Profile{"p": p})
	if len(permits) != 0 {
		t.Fatal("source disable bypassed by retirement")
	}
}
