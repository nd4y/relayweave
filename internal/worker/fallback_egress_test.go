package worker

import (
	"proxyplatform/internal/config"
	"testing"
)

func TestFallbackBalancesISPVariantsWithoutChangingProfilePriority(t *testing.T) {
	r := &Runtime{config: config.Config{EgressPaths: []config.EgressPath{
		{ID: "isp1", Enabled: true, Weight: 8, DownloadMbps: 100},
		{ID: "isp2", Enabled: true, Weight: 5, DownloadMbps: 100},
	}}}
	p := &policyState{config: config.Policy{Mode: "fallback", ActivePaths: 1}}
	a, b, reserve := readyRoute("p1-isp1"), readyRoute("p1-isp2"), readyRoute("p2-isp2")
	a.Config.ProfileID, b.Config.ProfileID, reserve.Config.ProfileID = "p1", "p1", "p2"
	a.Config.EgressID, b.Config.EgressID, reserve.Config.EgressID = "isp1", "isp2", "isp2"
	counts := map[string]int{}
	for i := 0; i < 200; i++ {
		selected := r.selectFallbackEgressLocked(p, []*Route{a, b, reserve}, nil)
		if len(selected) != 3 || selected[0].Config.Profile() != "p1" || selected[1].Config.Profile() != "p1" || selected[2] != reserve {
			t.Fatal("fallback profile order changed")
		}
		counts[selected[0].Config.EgressID]++
	}
	if counts["isp1"] != 100 || counts["isp2"] != 100 {
		t.Fatal("logical profile pinned an ISP", counts)
	}
	r.config.EgressPaths[0].Enabled = false
	for i := 0; i < 20; i++ {
		selected := r.selectFallbackEgressLocked(p, []*Route{a, b, reserve}, nil)
		if len(selected) != 2 || selected[0] != b || selected[1] != reserve {
			t.Fatal("disabled ISP admitted or fallback priority lost")
		}
	}
}
