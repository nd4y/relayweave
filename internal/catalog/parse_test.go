package catalog

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

const fixtureURI = "vless://00000000-0000-4000-8000-000000000001@example.invalid:443?security=reality&sni=example.org&fp=chrome&pbk=public&sid=abcd&type=tcp#DE%20Test"

func TestURIAndBase64KeepSecurity(t *testing.T) {
	for _, s := range []string{fixtureURI, base64.StdEncoding.EncodeToString([]byte(fixtureURI))} {
		p, e := Parse([]byte(s))
		if e != nil {
			t.Fatal(e)
		}
		v := p.Profiles[0]
		if v.Host != "example.invalid" || v.Security != "reality" || v.Country != "DE" || !v.Supported {
			t.Fatalf("bad profile %+v", v)
		}
		if !strings.Contains(string(v.Outbound), `"publicKey":"public"`) {
			t.Fatal("lost reality key")
		}
	}
}
func TestDedupeIdentityIncludesCredentials(t *testing.T) {
	a, e := Parse([]byte(fixtureURI))
	if e != nil {
		t.Fatal(e)
	}
	b, e := Parse([]byte(strings.Replace(fixtureURI, "000000000001", "000000000002", 1)))
	if e != nil {
		t.Fatal(e)
	}
	if a.Profiles[0].Fingerprint == b.Profiles[0].Fingerprint {
		t.Fatal("merged different credentials")
	}
	if a.Profiles[0].Identity != b.Profiles[0].Identity {
		t.Fatal("logical endpoint identity changed")
	}
}
func TestSubscriptionFamilies(t *testing.T) {
	cases := []string{
		`{"outbounds":[{"tag":"proxy","protocol":"trojan","settings":{"servers":[{"address":"test.invalid","port":443,"password":"fixture"}]},"streamSettings":{"network":"tcp","security":"tls"}}]}`,
		`{"outbounds":[{"tag":"DE","type":"vless","server":"test.invalid","server_port":443,"uuid":"fixture","tls":{"enabled":true,"server_name":"test.invalid"}}]}`,
		"proxies:\n  - name: DE\n    type: vless\n    server: test.invalid\n    port: 443\n    uuid: fixture\n    tls: true\n    servername: test.invalid\n",
		"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:fixture")) + "@test.invalid:443#DE",
		"vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"ps":"DE","add":"test.invalid","port":"443","id":"fixture","aid":"0","net":"ws","tls":"tls","path":"/test"}`)),
	}
	for _, s := range cases {
		p, e := Parse([]byte(s))
		if e != nil || len(p.Profiles) != 1 || !p.Profiles[0].Supported {
			t.Fatalf("failed family: %v %s", e, s)
		}
		if !json.Valid(p.Profiles[0].Outbound) {
			t.Fatal("invalid outbound")
		}
	}
}
func TestRejectHTMLRecursiveYAMLAndFileImports(t *testing.T) {
	for _, s := range []string{"<html>login</html>", "proxies: &self\n  - *self\n", "{}"} {
		if _, e := Parse([]byte(s)); e == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	p, e := Parse([]byte(`{"outbounds":[{"protocol":"vless","settings":{"address":"test.invalid","port":443,"id":"fixture"},"streamSettings":{"tlsSettings":{"certificates":[{"certificateFile":"/etc/private"}]}}}]}`))
	if e != nil {
		t.Fatal(e)
	}
	if p.Profiles[0].Supported {
		t.Fatal("trusted subscription filesystem path")
	}
}
func TestFiltersAndManualExclusions(t *testing.T) {
	f := Filter{Mode: "all", Conditions: []Condition{{Field: "name", Operator: "contains", Value: "LTE"}, {Field: "country", Operator: "not_equals", Value: "RU"}}, ExcludedIDs: []string{"manual"}}
	if e := f.Validate(); e != nil {
		t.Fatal(e)
	}
	p := Profile{Name: "DE LTE", Country: "DE"}
	if ok, _ := f.Match("keep", p); !ok {
		t.Fatal("failed match")
	}
	if ok, reason := f.Match("manual", p); ok || reason != "manual_exclusion" {
		t.Fatal("ignored manual override")
	}
	if (Filter{Conditions: []Condition{{Field: "name", Operator: "regex", Value: "(?=unsupported)"}}}).Validate() == nil {
		t.Fatal("accepted unsupported lookahead")
	}
}
func FuzzParse(f *testing.F) {
	f.Add([]byte(fixtureURI))
	f.Add([]byte(`{"outbounds":[]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 32<<10 {
			return
		}
		Parse(b)
	})
}
