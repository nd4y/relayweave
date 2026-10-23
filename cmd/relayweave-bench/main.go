// relayweave-bench generates explicitly bounded application traffic through a
// configured HTTP(S)/SOCKS5 proxy. Endpoints/credentials are read from environment
// and never included in reports. It is not an automatic background speed probe.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type allowance struct {
	used  atomic.Int64
	limit int64
}

func (b *allowance) reserve(n int64) bool {
	for {
		old := b.used.Load()
		if n <= 0 || n > b.limit-old {
			return false
		}
		if b.used.CompareAndSwap(old, old+n) {
			return true
		}
	}
}

type counters struct{ payload, valid, completed, failed atomic.Int64 }
type countedReader struct {
	io.Reader
	count *atomic.Int64
	local *atomic.Int64
}

func (r countedReader) Read(p []byte) (int, error) {
	n, e := r.Reader.Read(p)
	r.count.Add(int64(n))
	if r.local != nil {
		r.local.Add(int64(n))
	}
	return n, e
}

type repeated struct {
	block []byte
	at    int
}

func (r *repeated) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		k := copy(p[n:], r.block[r.at:])
		n += k
		r.at = (r.at + k) % len(r.block)
	}
	return n, nil
}

type config struct {
	duration        time.Duration
	maxBytes, chunk int64
	parallel        int
	adaptive        bool
	upload          bool
	allowHTTP       bool
	latencyURL      string
}

func transport(proxy string) (*http.Transport, error) {
	t := &http.Transport{DisableKeepAlives: true, DisableCompression: true, MaxIdleConns: 0, MaxConnsPerHost: 32,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ClientSessionCache: tls.NewLRUClientSessionCache(64)}}
	if proxy != "" {
		u, e := url.Parse(proxy)
		if e != nil || u.Host == "" {
			return nil, errors.New("invalid proxy setting")
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, errors.New("unsupported proxy protocol")
		}
		t.Proxy = http.ProxyURL(u)
	}
	return t, nil
}
func endpoint(raw string, n int64, allowHTTP bool) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) {
		return "", errors.New("invalid endpoint setting")
	}
	q := u.Query()
	q.Set("bytes", strconv.FormatInt(n, 10))
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func transfer(ctx context.Context, c *http.Client, target string, n int64, upload bool, block []byte, out *counters) error {
	var body io.Reader
	var sent atomic.Int64
	method := "GET"
	if upload {
		method = "POST"
		body = countedReader{Reader: io.LimitReader(&repeated{block: block}, n), count: &out.payload, local: &sent}
	}
	req, e := http.NewRequestWithContext(ctx, method, target, body)
	if e != nil {
		return errors.New("request setup failed")
	}
	if upload {
		req.ContentLength = n
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	r, e := c.Do(req)
	if e != nil {
		return errors.New("request failed")
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return errors.New("endpoint rejected request")
	}
	if r.Uncompressed || (r.Header.Get("Content-Encoding") != "" && r.Header.Get("Content-Encoding") != "identity") {
		return errors.New("compressed response")
	}
	if upload {
		_, e = io.Copy(io.Discard, io.LimitReader(r.Body, 4097))
		if e != nil {
			return errors.New("upload response failed")
		}
		if sent.Load() != n {
			return errors.New("upload not fully consumed")
		}
	} else {
		var got int64
		got, e = io.Copy(io.Discard, countedReader{Reader: io.LimitReader(r.Body, n+1), count: &out.payload})
		if e != nil || got != n {
			return errors.New("download length mismatch")
		}
	}
	out.valid.Add(n)
	out.completed.Add(1)
	return nil
}
func quantile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	v = append([]float64(nil), v...)
	sort.Float64s(v)
	return v[max(0, int(math.Ceil(q*float64(len(v))))-1)]
}

// Saturation discovery is bounded. A failed larger step falls back and stops
// growing rather than oscillating forever or enabling more than sixteen flows.
func nextParallel(current int, previousRate, rate, p95 float64) (int, bool) {
	if previousRate > 0 && (rate < previousRate*1.05 || p95 > .2) {
		return max(1, current/2), false
	}
	if p95 > .2 {
		return max(1, current/2), false
	}
	if current >= 16 || p95 == 0 {
		return current, false
	}
	return min(16, current*2), true
}

func run(ctx context.Context, cfg config, proxy, raw string, w io.Writer) error {
	if cfg.duration < time.Second || cfg.duration > time.Hour || cfg.maxBytes <= 0 || cfg.chunk < 65536 || cfg.chunk > 64<<20 || cfg.parallel < 1 || cfg.parallel > 16 {
		return errors.New("invalid duration, byte cap, chunk or concurrency")
	}
	target, e := endpoint(raw, cfg.chunk, cfg.allowHTTP)
	if e != nil {
		return e
	}
	pingRaw := raw
	if cfg.latencyURL != "" {
		pingRaw = cfg.latencyURL
	}
	pingTarget, e := endpoint(pingRaw, 0, cfg.allowHTTP)
	if e != nil {
		return e
	}
	t, e := transport(proxy)
	if e != nil {
		return e
	}
	defer t.CloseIdleConnections()
	client := &http.Client{Transport: t, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(ctx, cfg.duration)
	defer cancel()
	start := time.Now()
	budget := &allowance{limit: cfg.maxBytes}
	out := &counters{}
	block := make([]byte, 64<<10)
	if _, e = rand.Read(block); e != nil {
		return errors.New("payload initialization failed")
	}
	var parallel atomic.Int64
	parallel.Store(int64(cfg.parallel))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for ctx.Err() == nil {
				if int64(index) >= parallel.Load() {
					select {
					case <-ctx.Done():
						return
					case <-time.After(100 * time.Millisecond):
						continue
					}
				}
				if !budget.reserve(cfg.chunk + 4097) {
					cancel()
					return
				}
				err := transfer(ctx, client, target, cfg.chunk, cfg.upload, block, out)
				if err != nil && ctx.Err() == nil {
					out.failed.Add(1)
					select {
					case <-ctx.Done():
						return
					case <-time.After(250 * time.Millisecond):
					}
				}
			}
		}(i)
	}
	latency := make(chan float64, 4)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !budget.reserve(4097) {
					cancel()
					return
				}
				began := time.Now()
				request, stop := context.WithTimeout(ctx, 5*time.Second)
				// The latency request is always GET, including during upload measurement.
				ping := &counters{}
				err := transfer(request, client, pingTarget, 0, false, nil, ping)
				stop()
				if err != nil {
					if ctx.Err() == nil {
						out.failed.Add(1)
					}
					continue
				}
				select {
				case latency <- time.Since(began).Seconds():
				default:
				}
			}
		}
	}()
	encode := json.NewEncoder(w)
	if e = encode.Encode(map[string]any{"event": "start", "at": start.UTC(), "direction": map[bool]string{false: "download", true: "upload"}[cfg.upload], "duration_seconds": cfg.duration.Seconds(), "max_application_reserved_bytes": cfg.maxBytes, "adaptive": cfg.adaptive, "proxy_configured": proxy != ""}); e != nil {
		cancel()
		wg.Wait()
		return e
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := start
	lastBytes := int64(0)
	rates := []float64{}
	pings := []float64{}
	stepPings := []float64{}
	stepStart, stepBytes, previousRate := start, int64(0), float64(0)
	latencyLimit := float64(0)
	growing := cfg.adaptive
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case p := <-latency:
			pings = append(pings, p)
			stepPings = append(stepPings, p)
		case now := <-ticker.C:
			total := out.payload.Load()
			rate := float64(total-lastBytes) * 8 / now.Sub(last).Seconds() / 1e6
			rates = append(rates, rate)
			last, lastBytes = now, total
			e = encode.Encode(map[string]any{"event": "sample", "elapsed_seconds": now.Sub(start).Seconds(), "mbps": rate, "payload_bytes": total, "valid_bytes": out.valid.Load(), "failures": out.failed.Load(), "parallel": parallel.Load(), "latency_p95_seconds": quantile(stepPings, .95)})
			if e != nil {
				cancel()
				break
			}
			if growing && now.Sub(stepStart) >= 30*time.Second {
				stepRate := float64(total-stepBytes) * 8 / now.Sub(stepStart).Seconds() / 1e6
				p95 := quantile(stepPings, .95)
				if latencyLimit == 0 && p95 > 0 {
					latencyLimit = p95 + math.Max(.05, p95*.25)
				}
				normalizedLatency := float64(0)
				if latencyLimit > 0 {
					normalizedLatency = .2 * p95 / latencyLimit
				}
				next, keepGrowing := nextParallel(int(parallel.Load()), previousRate, stepRate, normalizedLatency)
				parallel.Store(int64(next))
				growing = keepGrowing
				previousRate = stepRate
				stepStart, stepBytes, stepPings = now, total, nil
			}
		}
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	windows := []float64{}
	for i := 0; i+10 <= len(rates); i++ {
		sum := 0.0
		for _, v := range rates[i : i+10] {
			sum += v
		}
		windows = append(windows, sum/10)
	}
	reason := "duration"
	if budget.limit-budget.used.Load() < cfg.chunk+4097 {
		reason = "byte_cap"
	}
	if elapsed < cfg.duration.Seconds()-.1 && reason != "byte_cap" {
		reason = "cancelled"
	}
	summary := map[string]any{"event": "summary", "seconds": elapsed, "stop_reason": reason, "payload_bytes": out.payload.Load(), "valid_bytes": out.valid.Load(), "reserved_application_bytes": budget.used.Load(), "completed": out.completed.Load(), "failures": out.failed.Load(), "average_mbps": float64(out.valid.Load()) * 8 / elapsed / 1e6, "average_payload_mbps": float64(out.payload.Load()) * 8 / elapsed / 1e6, "ten_second_p05_payload_mbps": quantile(windows, .05), "ten_second_median_payload_mbps": quantile(windows, .5), "latency_p50_seconds": quantile(pings, .5), "latency_p95_seconds": quantile(pings, .95), "latency_p99_seconds": quantile(pings, .99), "latency_sample_count": len(pings), "final_parallel": parallel.Load()}
	if e != nil {
		return e
	}
	if e = encode.Encode(summary); e != nil {
		return e
	}
	if out.completed.Load() == 0 || out.failed.Load() > 0 {
		return errors.New("workload contains failed requests; inspect public summary")
	}
	return nil
}

func main() {
	var cfg config
	flag.DurationVar(&cfg.duration, "duration", time.Minute, "wall-clock workload duration, <=1h")
	flag.Int64Var(&cfg.maxBytes, "max-bytes", 0, "required conservative application byte reservation cap; no refund")
	flag.Int64Var(&cfg.chunk, "chunk-bytes", 16<<20, "bytes per transfer")
	flag.IntVar(&cfg.parallel, "parallel", 4, "initial concurrency, 1..16")
	flag.BoolVar(&cfg.adaptive, "adaptive", false, "bounded automatic concurrency search")
	flag.BoolVar(&cfg.upload, "upload", false, "POST upload instead of download")
	flag.BoolVar(&cfg.allowHTTP, "allow-http", false, "allow explicit isolated test HTTP origin")
	flag.Parse()
	raw := os.Getenv("RELAYWEAVE_BENCH_DOWNLOAD_URL")
	cfg.latencyURL = raw
	if cfg.upload {
		raw = os.Getenv("RELAYWEAVE_BENCH_UPLOAD_URL")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := run(ctx, cfg, os.Getenv("RELAYWEAVE_BENCH_PROXY"), raw, os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "benchmark failed:", e)
		os.Exit(1)
	}
}
