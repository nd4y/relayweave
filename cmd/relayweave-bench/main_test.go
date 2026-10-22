package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentReservationCannotExceedCap(t *testing.T) {
	b := &allowance{limit: 1000}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b.reserve(7) {
			}
		}()
	}
	wg.Wait()
	if b.used.Load() != 994 || b.reserve(7) || b.reserve(-1) {
		t.Fatal(b.used.Load())
	}
}

func TestBoundedRealHTTPUploadDownloadViaProxy(t *testing.T) {
	for _, upload := range []bool{false, true} {
		t.Run(strconv.FormatBool(upload), func(t *testing.T) {
			var proxyCalls int
			var mu sync.Mutex
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Host != "origin.example" {
					t.Error("absolute proxy target lost")
				}
				mu.Lock()
				proxyCalls++
				mu.Unlock()
				n, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
				if r.Method == "POST" {
					got, e := io.Copy(io.Discard, r.Body)
					if e != nil || got != n {
						w.WriteHeader(400)
						return
					}
					w.WriteHeader(200)
					return
				}
				w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
				io.CopyN(w, &repeated{block: []byte("payload")}, n)
			}))
			defer proxy.Close()
			var output bytes.Buffer
			e := run(context.Background(), config{duration: time.Second, maxBytes: 1 << 20, chunk: 65536, parallel: 1, upload: upload, allowHTTP: true}, proxy.URL, "http://origin.example/transfer?token=secret-not-for-report", &output)
			if e != nil {
				t.Fatal(e)
			}
			if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "origin.example") || strings.Contains(output.String(), proxy.URL) {
				t.Fatal("endpoint exposed in report")
			}
			var summary map[string]any
			for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
				var row map[string]any
				if e = json.Unmarshal([]byte(line), &row); e != nil {
					t.Fatal(e)
				}
				if row["event"] == "summary" {
					summary = row
				}
			}
			if summary["reserved_application_bytes"].(float64) > 1<<20 || summary["completed"].(float64) < 1 || summary["stop_reason"] != "byte_cap" {
				t.Fatal(summary)
			}
			mu.Lock()
			defer mu.Unlock()
			if proxyCalls < 1 {
				t.Fatal("proxy bypassed")
			}
		})
	}
}

func TestConcurrencyStopsAfterPlateauOrLatencyRegression(t *testing.T) {
	for _, row := range []struct {
		n                int
		before, now, p95 float64
		want             int
		grow             bool
	}{
		{4, 0, 200, .05, 8, true}, {8, 200, 202, .05, 4, false}, {16, 200, 150, .05, 8, false}, {4, 0, 100, .5, 2, false}, {4, 0, 100, 0, 4, false},
	} {
		n, g := nextParallel(row.n, row.before, row.now, row.p95)
		if n != row.want || g != row.grow {
			t.Fatal(row, n, g)
		}
	}
}

func TestRejectRedirectAndShortBody(t *testing.T) {
	for _, redirect := range []bool{true, false} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if redirect {
				http.Redirect(w, r, "http://other.example", 302)
			} else {
				w.Write([]byte("short"))
			}
		}))
		tr, e := transport("")
		if e != nil {
			t.Fatal(e)
		}
		client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if e = transfer(context.Background(), client, s.URL, 100, false, nil, &counters{}); e == nil {
			t.Fatal("invalid transfer accepted")
		}
		s.Close()
		tr.CloseIdleConnections()
	}
}

func TestUploadLatencyUsesDownloadEndpoint(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			if r.URL.Path != "/down" {
				w.WriteHeader(405)
			}
			return
		}
		io.Copy(io.Discard, r.Body)
		time.Sleep(100 * time.Millisecond)
	}))
	defer proxy.Close()
	var output bytes.Buffer
	if e := run(context.Background(), config{duration: 1200 * time.Millisecond, maxBytes: 100 << 20, chunk: 65536, parallel: 1, upload: true, allowHTTP: true, latencyURL: "http://origin.example/down"}, proxy.URL, "http://origin.example/up", &output); e != nil {
		t.Fatal(e)
	}
	var summary map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var row map[string]any
		json.Unmarshal([]byte(line), &row)
		if row["event"] == "summary" {
			summary = row
		}
	}
	if summary["latency_sample_count"].(float64) < 1 || summary["failures"].(float64) != 0 {
		t.Fatal("upload used POST-only endpoint for latency", summary)
	}
}
