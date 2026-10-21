package config_test

import (
	"strings"
	"testing"
	"time"

	"proxyplatform/internal/catalog"
	"proxyplatform/internal/config"
)

func TestLiveGuardAcceptsActualImportedProfileFingerprint(t *testing.T) {
	parsed, err := catalog.Parse([]byte("vless://00000000-0000-0000-0000-000000000001@proxy.invalid:443?security=tls&type=tcp#fixture"))
	if err != nil || len(parsed.Profiles) != 1 {
		t.Fatalf("fixture parse: %v", err)
	}
	fp := parsed.Profiles[0].Fingerprint
	if len(fp) != 32 {
		t.Fatal("unexpected catalogue identity format")
	}
	g := config.LiveGuard{Version: 1, Routes: map[string]config.RoutePermit{"route": {Fingerprint: fp, ValidUntil: time.Now().UTC().Add(time.Hour)}}}
	if err := g.Validate(); err != nil {
		t.Fatalf("actual catalogue fingerprint rejected: %v", err)
	}
	for _, fingerprint := range []string{strings.Repeat("a", 64), "", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("z", 32)} {
		permit := g.Routes["route"]
		permit.Fingerprint = fingerprint
		g.Routes["route"] = permit
		err := g.Validate()
		if (err == nil) != (len(fingerprint) == 64) {
			t.Fatalf("fingerprint validation mismatch: length=%d error=%v", len(fingerprint), err)
		}
	}
}
