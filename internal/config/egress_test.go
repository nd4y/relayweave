package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEgressExpansionIsolationAndDisable(t *testing.T) {
	c, e := Decode(strings.NewReader(valid))
	if e != nil {
		t.Fatal(e)
	}
	c.Policies = []Policy{{ID: "p", Mode: "auto", Routes: []string{"one"}, ActivePaths: 2}}
	c.EgressPaths = []EgressPath{{ID: "isp1", SourceIP: "127.0.0.2", Enabled: true, Weight: 8, DownloadMbps: 760, UploadMbps: 760}, {ID: "isp2", SourceIP: "127.0.0.3", Enabled: true, Weight: 5, DownloadMbps: 475, UploadMbps: 475}}
	original := string(c.Routes[0].Outbound)
	if e = c.ExpandEgress(); e != nil {
		t.Fatal(e)
	}
	if e = c.Validate(); e != nil {
		t.Fatal(e)
	}
	if len(c.Routes) != 2 || len(c.Policies[0].Routes) != 2 || c.Routes[0].ID == c.Routes[1].ID || c.Routes[0].Profile() != "one" {
		t.Fatal("path identities lost")
	}
	for _, r := range c.Routes {
		var o map[string]any
		json.Unmarshal(r.Outbound, &o)
		p, _ := c.Egress(r.EgressID)
		if o["sendThrough"] != p.SourceIP {
			t.Fatal("wrong source")
		}
	}
	if original == string(c.Routes[0].Outbound) {
		t.Fatal("source not enforced")
	}
	c.Routes[0].EgressID = "missing"
	if c.Validate() == nil {
		t.Fatal("unknown path accepted")
	}
	c, e = Decode(strings.NewReader(valid))
	if e != nil {
		t.Fatal(e)
	}
	c.EgressPaths = []EgressPath{{ID: "isp1", SourceIP: "127.0.0.2", Enabled: false, Weight: 8, DownloadMbps: 760, UploadMbps: 760}, {ID: "isp2", SourceIP: "127.0.0.3", Enabled: true, Weight: 5, DownloadMbps: 475, UploadMbps: 475}}
	if e = c.ExpandEgress(); e != nil || len(c.Routes) != 1 || c.Routes[0].EgressID != "isp2" {
		t.Fatal("disabled ISP expanded", e)
	}
}

func TestEgressRejectsAmbiguousBindings(t *testing.T) {
	for _, out := range []string{`{"protocol":"vless","streamSettings":{"sockopt":{"interface":"eth0"}}}`, `{"protocol":"socks","proxySettings":{"tag":"bypass"}}`, `{"protocol":"vless","streamSettings":{"sockopt":{"mark":42}}}`} {
		c, _ := Decode(strings.NewReader(valid))
		c.Routes[0].Outbound = json.RawMessage(out)
		c.EgressPaths = []EgressPath{{ID: "a", SourceIP: "127.0.0.2", Enabled: true, Weight: 1, DownloadMbps: 1, UploadMbps: 1}}
		if c.ExpandEgress() == nil {
			t.Fatal("accepted ambiguous binding")
		}
	}
	for _, ip := range []string{"", "0.0.0.0", "224.0.0.1", "not-an-ip"} {
		if ValidateEgressPaths([]EgressPath{{ID: "a", SourceIP: ip, Enabled: true, Weight: 1, DownloadMbps: 1, UploadMbps: 1}}) == nil {
			t.Fatal("invalid source accepted")
		}
	}
}
