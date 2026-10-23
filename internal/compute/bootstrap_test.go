package compute

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapAndConfigurationFailClosed(t *testing.T) {
	dir := t.TempDir()
	if e := Bootstrap(dir); e != nil {
		t.Fatal(e)
	}
	if _, e := computeAuthority(dir); e != nil {
		t.Fatal(e)
	}
	if _, e := computeAuthority(t.TempDir()); e == nil {
		t.Fatal("adopted missing/data-plane authority")
	}
	var keys map[string][]byte
	raw, e := os.ReadFile(filepath.Join(dir, "recommendation-keys.json"))
	if e != nil || json.Unmarshal(raw, &keys) != nil || len(keys) != 1 {
		t.Fatal("public key export", e)
	}
	if e = os.Chmod(filepath.Join(dir, "signing.seed"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = ReadKey(filepath.Join(dir, "signing.seed")); e == nil {
		t.Fatal("world-readable secret accepted")
	}
	for _, address := range []string{"http://localhost", "https://user:secret@localhost", "https://localhost/path", "https://localhost?query=1", "https://localhost#fragment"} {
		if _, e = NewClient(address, &tls.Config{}); e == nil {
			t.Fatal("unsafe dispatcher", address)
		}
	}
	if _, e = NewClient("https://localhost", &tls.Config{InsecureSkipVerify: true}); e == nil {
		t.Fatal("insecure TLS accepted")
	}
	for _, listen := range []string{"0.0.0.0:9911", "[::]:9911", "8.8.8.8:9911", "hostname:9911"} {
		cfg := ServiceConfig{Version: 1, Enabled: true, Installation: "install", Listen: listen, IdentityDir: dir, SigningSeedFile: "seed", TokenKeyFile: "token"}
		data, _ := json.Marshal(cfg)
		path := filepath.Join(t.TempDir(), "config.json")
		_ = os.WriteFile(path, data, 0600)
		if _, e = ReadServiceConfig(path); e == nil {
			t.Fatal("public/implicit bind accepted", listen)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := sleep(ctx, 1000000000); e {
		t.Fatal("shutdown slept")
	}
}

func FuzzStrictDecode(f *testing.F) {
	for _, raw := range []string{`{"a":1}`, `{"a":1,"a":2}`, `[]`, `{"x":1e99}`, `null`} {
		f.Add([]byte(raw))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var v any
		if StrictDecode(raw, &v) == nil {
			canonical, e := Canonical(v)
			if e != nil {
				t.Fatal(e)
			}
			var check any
			if StrictDecode(canonical, &check) != nil {
				t.Fatal("canonical does not decode")
			}
		}
	})
}

func BenchmarkComputeSearch16(b *testing.B) {
	s := planFixture()
	s.Groups = nil
	ids := []string{}
	for i := 0; i < 16; i++ {
		id := string(rune('a' + i))
		ids = append(ids, id)
		s.Groups = append(s.Groups, Group{ID: id, Lane: "lane", Baseline: 62500, RTTMS: uint32(20 + i*30), Samples: 100, CapacityBPS: 10000000})
	}
	s.RouteSet, _ = Hash(ids)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, e := Search(context.Background(), s, 0, 64); e != nil {
			b.Fatal(e)
		}
	}
}
