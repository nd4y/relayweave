package worker

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type qualityDirectTestAdapter struct{ core.XrayAdapter }

func (qualityDirectTestAdapter) Family() string { return "quality-direct-test" }
func (qualityDirectTestAdapter) OpenTCP(ctx context.Context, endpoint, _ string, timeout time.Duration) (net.Conn, error) {
	return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", endpoint)
}

var qualityAdapterOnce sync.Once

func qualityTestRoute(server *httptest.Server) *Route {
	qualityAdapterOnce.Do(func() {
		if err := core.RegisterAdapter(qualityDirectTestAdapter{}); err != nil {
			panic(err)
		}
	})
	return &Route{Config: config.Route{Endpoint: server.Listener.Addr().String()}, Instance: &core.Instance{Config: config.Core{Family: "quality-direct-test"}}}
}

func TestControlledQualityHTTPMeasuresHeadersRejectsRedirectAndBoundsBodies(t *testing.T) {
	var redirected atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(204)
		case "/redirect":
			w.Header().Set("Location", "http://controlled.example/followed")
			w.WriteHeader(302)
		case "/followed":
			redirected.Add(1)
			w.WriteHeader(204)
		case "/bulk":
			w.Header().Set("Content-Length", "1048576")
			io.CopyN(w, strings.NewReader(strings.Repeat("a", int(qualityBulkPayload))), qualityBulkPayload)
		case "/oversized":
			w.Header().Set("Content-Length", "2097152")
			w.WriteHeader(200)
		case "/short":
			w.Header().Set("Content-Length", "1048576")
			w.Write([]byte("short"))
		case "/headers":
			w.Header().Set("Too-Large", strings.Repeat("x", 40<<10))
			w.WriteHeader(200)
		case "/slow":
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
				w.WriteHeader(204)
			}
		}
	}))
	defer server.Close()
	route := qualityTestRoute(server)
	for _, tt := range []struct {
		path     string
		bulk, ok bool
	}{{"/health", false, true}, {"/redirect", false, false}, {"/bulk", true, true}, {"/oversized", true, false}, {"/short", true, false}, {"/headers", false, false}, {"/slow", false, false}} {
		t.Run(tt.path, func(t *testing.T) {
			m := measureRolloverHTTP(context.Background(), route, "http://controlled.example"+tt.path, tt.bulk, 200*time.Millisecond)
			if m.OK != tt.ok {
				t.Fatal(m)
			}
			if m.OK && (m.TTFBUS <= 0 || m.ElapsedUS <= 0) {
				t.Fatal("missing actual HTTP timings", m)
			}
			if m.Bytes > qualityBulkPayload {
				t.Fatal("unbounded body", m)
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("probe followed a redirect")
	}
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer secure.Close()
	if m := measureRolloverHTTP(context.Background(), qualityTestRoute(secure), "https://controlled.example/health", false, time.Second); m.OK {
		t.Fatal("untrusted TLS accepted", m)
	}
}

type qualityCountConn struct{ bytes atomic.Int64 }

func (c *qualityCountConn) Read(p []byte) (int, error) {
	clear(p)
	c.bytes.Add(int64(len(p)))
	return len(p), nil
}
func (c *qualityCountConn) Write(p []byte) (int, error) {
	c.bytes.Add(int64(len(p)))
	return len(p), nil
}
func (*qualityCountConn) Close() error                     { return nil }
func (*qualityCountConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*qualityCountConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*qualityCountConn) SetDeadline(time.Time) error      { return nil }
func (*qualityCountConn) SetReadDeadline(time.Time) error  { return nil }
func (*qualityCountConn) SetWriteDeadline(time.Time) error { return nil }

func TestControlledProbeConcurrentReadWriteShareOneAllowance(t *testing.T) {
	underlying := &qualityCountConn{}
	bounded := &qualityBudgetConn{Conn: underlying}
	bounded.remaining.Store(10000)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(read bool) {
			defer wg.Done()
			buf := make([]byte, 127)
			for {
				var err error
				if read {
					_, err = bounded.Read(buf)
				} else {
					_, err = bounded.Write(buf)
				}
				if err != nil {
					return
				}
			}
		}(i%2 == 0)
	}
	wg.Wait()
	if underlying.bytes.Load() != 10000 || bounded.remaining.Load() != 0 {
		t.Fatal("read/write duplicated allowance", underlying.bytes.Load(), bounded.remaining.Load())
	}
}
