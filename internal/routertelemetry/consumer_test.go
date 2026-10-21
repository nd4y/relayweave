package routertelemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func consumerDocument(at time.Time) Document {
	rx, tx := 100.0, 200.0
	return Document{Version: 1, Mode: "observe", Enabled: true, Routers: []Snapshot{{RouterID: "home", Fresh: true, State: "ready", ObservedAt: at, Capabilities: []string{"wan.counters", "wan.link_state"}, Reading: Reading{WANs: []WAN{{ID: "wan1", ISP: "isp1", Interface: "wan", Running: true, Quality: "measured", DownloadBPS: &rx, UploadBPS: &tx}}}}}}
}

func TestConsumerFreshStaleFailureAndLinkDown(t *testing.T) {
	var body atomic.Value
	put := func(doc Document) { b, _ := json.Marshal(doc); body.Store(b) }
	put(consumerDocument(time.Now()))
	token := strings.Repeat("fixture-", 6)
	path := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(path, []byte(token), 0600)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(401)
			return
		}
		w.Write(body.Load().([]byte))
	}))
	defer server.Close()
	c, e := NewConsumer(server.URL+"/v1/snapshot", path)
	if e != nil {
		t.Fatal(e)
	}
	c.Refresh(context.Background())
	s, ok := c.Signal("isp1", time.Now())
	if !ok || s.LinkDown || s.DownloadBPS == nil {
		t.Fatal("fresh sample lost")
	}
	if _, ok = c.Signal("isp1", time.Now().Add(11*time.Second)); ok {
		t.Fatal("stale cache used")
	}
	d := consumerDocument(time.Now())
	d.Routers[0].WANs[0].Running = false
	d.Routers[0].WANs[0].Quality = "link_down"
	put(d)
	c.Refresh(context.Background())
	s, ok = c.Signal("isp1", time.Now())
	if !ok || !s.LinkDown || s.DownloadBPS != nil {
		t.Fatal("link state confused with idle speed")
	}
	put(consumerDocument(time.Now().Add(-time.Minute)))
	c.Refresh(context.Background())
	if _, ok = c.Signal("isp1", time.Now()); ok {
		t.Fatal("producer stale accepted")
	}
	put(consumerDocument(time.Now().Add(time.Minute)))
	c.Refresh(context.Background())
	if _, ok = c.Signal("isp1", time.Now()); ok {
		t.Fatal("future clock accepted")
	}
	d = consumerDocument(time.Now())
	bad := -1.0
	d.Routers[0].WANs[0].DownloadBPS = &bad
	put(d)
	c.Refresh(context.Background())
	if c.Status(time.Now()).State != "unavailable" {
		t.Fatal("invalid sample accepted")
	}
	body.Store([]byte(strings.Repeat("x", 129*1024)))
	c.Refresh(context.Background())
	if _, ok = c.Signal("isp1", time.Now()); ok {
		t.Fatal("oversized response used")
	}
}

func TestConsumerDoesNotFollowRedirectsOrAcceptRemoteEndpoints(t *testing.T) {
	for _, endpoint := range []string{"http://192.0.2.1/v1/snapshot", "http://localhost/v1/snapshot", "http://127.0.0.1/v1/snapshot?token=secret", "http://user:pass@127.0.0.1/v1/snapshot"} {
		if _, e := NewConsumer(endpoint, "file"); e == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	path := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(path, []byte(strings.Repeat("fixture", 8)), 0600)
	c, e := NewConsumer(redirect.URL+"/v1/snapshot", path)
	if e != nil {
		t.Fatal(e)
	}
	c.Refresh(context.Background())
	if calls.Load() != 0 || c.Status(time.Now()).State != "unavailable" {
		t.Fatal("redirect reached another service")
	}
}

func TestConsumerRefreshFailsClosedOnAmbiguousDocuments(t *testing.T) {
	// Independently reviewed test draft: seed valid data, then prove a rejected
	// document also removes the previously selectable advisory input.
	for _, name := range []string{"duplicate ISP", "duplicate interface", "unsupported version"} {
		t.Run(name, func(t *testing.T) {
			var body atomic.Value
			put := func(d Document) {
				b, e := json.Marshal(d)
				if e != nil {
					t.Fatal(e)
				}
				body.Store(b)
			}
			put(consumerDocument(time.Now().Add(-time.Second)))
			token := strings.Repeat("fixture-", 6)
			path := filepath.Join(t.TempDir(), "token")
			if e := os.WriteFile(path, []byte(token), 0600); e != nil {
				t.Fatal(e)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					w.WriteHeader(401)
					return
				}
				w.Write(body.Load().([]byte))
			}))
			defer server.Close()
			c, e := NewConsumer(server.URL+"/v1/snapshot", path)
			if e != nil {
				t.Fatal(e)
			}
			c.Refresh(context.Background())
			if _, ok := c.Signal("isp1", time.Now()); !ok {
				t.Fatal("seed missing")
			}
			d := consumerDocument(time.Now())
			switch name {
			case "duplicate ISP":
				other := d.Routers[0]
				other.RouterID = "branch"
				d.Routers = append(d.Routers, other)
			case "duplicate interface":
				other := d.Routers[0].WANs[0]
				other.ID = "wan2"
				other.ISP = "isp2"
				d.Routers[0].WANs = append(d.Routers[0].WANs, other)
			case "unsupported version":
				d.Version = 2
			}
			put(d)
			c.Refresh(context.Background())
			s := c.Status(time.Now())
			if s.State != "unavailable" || len(s.Signals) != 0 {
				t.Fatal("rejected document retained advisory state", s)
			}
			if _, ok := c.Signal("isp1", time.Now()); ok {
				t.Fatal("old selectable signal retained")
			}
		})
	}
}
