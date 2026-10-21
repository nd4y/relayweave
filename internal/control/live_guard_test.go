package control

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
)

func TestLivePermitsIntersectSourceFilterQuotaStalenessAndProfileState(t *testing.T) {
	now := time.Now().UTC()
	base := catalog.Source{Enabled: true, LastSuccess: now.Add(-time.Minute), MaxStaleSeconds: 300}
	profiles := map[string]catalog.Profile{}
	sources := []store.Object{}
	for _, name := range []string{"good", "disabled", "stale", "quota", "expired", "filtered", "absent", "unsupported", "profiledisabled"} {
		s := base
		p := catalog.Profile{SourceID: name, Present: true, Supported: true, Enabled: true, Fingerprint: strings.Repeat("a", 64), Country: "DE"}
		switch name {
		case "good":
			s.Quota = map[string]int64{"expire": now.Add(time.Minute).Unix()}
		case "disabled":
			s.Enabled = false
		case "stale":
			s.LastSuccess = now.Add(-time.Hour)
		case "quota":
			s.Quota = map[string]int64{"total": 10, "upload": 4, "download": 6}
		case "expired":
			s.Quota = map[string]int64{"expire": now.Add(-time.Second).Unix()}
		case "filtered":
			s.Filter.Countries = []string{"NL"}
		case "absent":
			p.Present = false
		case "unsupported":
			p.Supported = false
		case "profiledisabled":
			p.Enabled = false
		}
		b, _ := json.Marshal(s)
		sources = append(sources, store.Object{ID: name, Data: b})
		profiles[name] = p
	}
	permits, err := livePermits(now, sources, profiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(permits) != 1 || permits["good"].Fingerprint == "" || permits["good"].ValidUntil.Unix() != now.Add(time.Minute).Unix() {
		t.Fatal("live guard widened source/profile permissions or missed provider expiry")
	}
}
