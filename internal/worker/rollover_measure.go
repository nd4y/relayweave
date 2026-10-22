package worker

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Reserve before calling I/O. A concurrent TLS read/write cannot each consume
// the same remaining allowance. Unused per-call buffer capacity is returned only
// to this connection, never refunded to the durable daily reservation.
type qualityBudgetConn struct {
	net.Conn
	remaining atomic.Int64
}

func (c *qualityBudgetConn) take(size int) int {
	for {
		left := c.remaining.Load()
		if left <= 0 {
			return 0
		}
		n := min(int64(size), left)
		if c.remaining.CompareAndSwap(left, left-n) {
			return int(n)
		}
	}
}

func (c *qualityBudgetConn) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	n := c.take(len(buf))
	if n == 0 {
		return 0, fmt.Errorf("probe_read_budget_exhausted")
	}
	read, err := c.Conn.Read(buf[:n])
	c.remaining.Add(int64(n - read))
	return read, err
}

func (c *qualityBudgetConn) Write(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	n := c.take(len(buf))
	if n == 0 {
		return 0, fmt.Errorf("probe_write_budget_exhausted")
	}
	written, err := c.Conn.Write(buf[:n])
	c.remaining.Add(int64(n - written))
	return written, err
}

func pairedBulkTarget(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	// The configured default is a bounded generator, not a Range file server.
	// Request exactly the paired payload from its documented bytes parameter.
	if u.Scheme == "https" && strings.EqualFold(u.Hostname(), "speed.cloudflare.com") && (u.Port() == "" || u.Port() == "443") && u.Path == "/__down" {
		q := u.Query()
		q.Set("bytes", strconv.FormatInt(qualityBulkPayload, 10))
		u.RawQuery = q.Encode()
	}
	return u.String(), true
}

// Only this controlled request's headers define TTFB. No application TLS bytes
// are inspected. Core/proxy framing and kernel in-flight data remain distinct
// from a capped probe payload reservation and are not advertised as wire totals.
func measureRolloverHTTP(ctx context.Context, route *Route, target string, bulk bool, timeout time.Duration) qualityMeasurement {
	m := qualityMeasurement{}
	if route == nil || route.Instance == nil {
		return m
	}
	u, err := url.Parse(target)
	if err != nil || u.User != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return m
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	if forbiddenTarget(net.JoinHostPort(u.Hostname(), port)) {
		return m
	}
	limit := qualityHTTPReserve
	if bulk {
		limit = qualityBulkReserve
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	var connectUS atomic.Int64
	transport := &http.Transport{DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 16 << 10, ResponseHeaderTimeout: timeout, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: func(call context.Context, _, address string) (net.Conn, error) {
		at := time.Now()
		connection, e := route.Instance.Adapter().OpenTCP(call, route.Config.Endpoint, address, timeout)
		connectUS.Store(time.Since(at).Microseconds())
		if e != nil {
			return nil, e
		}
		bounded := &qualityBudgetConn{Conn: connection}
		bounded.remaining.Store(limit)
		return bounded, nil
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return m
	}
	request.Header.Set("Cache-Control", "no-cache")
	if bulk {
		request.Header.Set("Range", fmt.Sprintf("bytes=0-%d", qualityBulkPayload-1))
	}
	response, err := client.Do(request)
	m.ConnectUS = connectUS.Load()
	if err != nil {
		return m
	}
	defer response.Body.Close()
	m.TTFBUS = time.Since(started).Microseconds()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return m
	}
	if !bulk {
		// Headers are sufficient for a control health request; do not consume an
		// unbounded body merely because an origin ignored the intended endpoint.
		m.OK = true
		m.ElapsedUS = max(int64(1), time.Since(started).Microseconds())
		return m
	}
	if response.ContentLength >= 0 && response.ContentLength != qualityBulkPayload {
		return m
	}
	n, err := io.CopyN(io.Discard, response.Body, qualityBulkPayload)
	m.Bytes = n
	m.ElapsedUS = max(int64(1), time.Since(started).Microseconds())
	m.OK = err == nil && n == qualityBulkPayload
	return m
}
