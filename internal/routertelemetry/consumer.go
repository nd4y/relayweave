package routertelemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const ConsumerMaxAge = 10 * time.Second

// Signal is a normalized advisory input. It contains no router credentials or
// command access. Link state is distinct from Internet/proxy reachability.
type Signal struct {
	ISP         string    `json:"isp"`
	Router      string    `json:"router"`
	ObservedAt  time.Time `json:"observed_at"`
	Fresh       bool      `json:"fresh"`
	LinkKnown   bool      `json:"link_known"`
	LinkDown    bool      `json:"link_down"`
	DownloadBPS *float64  `json:"download_bytes_per_second"`
	UploadBPS   *float64  `json:"upload_bytes_per_second"`
}

type ConsumerStatus struct {
	Configured  bool      `json:"configured"`
	State       string    `json:"state"`
	LastSuccess time.Time `json:"last_success"`
	Signals     []Signal  `json:"signals"`
}

// Consumer polls a local collector outside the scheduling lock/hot path. There
// are no router calls, redirects or environment proxy settings in this client.
type Consumer struct {
	endpoint    string
	tokenFile   string
	client      *http.Client
	mu          sync.RWMutex
	state       string
	lastSuccess time.Time
	receivedAt  time.Time
	signals     map[string]Signal
}

func NewConsumer(endpoint, tokenFile string) (*Consumer, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/v1/snapshot" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || tokenFile == "" {
		return nil, errors.New("router telemetry requires a loopback snapshot endpoint and token file")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext, ResponseHeaderTimeout: 350 * time.Millisecond, MaxConnsPerHost: 1, IdleConnTimeout: 15 * time.Second}
	return &Consumer{endpoint: endpoint, tokenFile: tokenFile, state: "starting", signals: map[string]Signal{}, client: &http.Client{Transport: transport, Timeout: 600 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("telemetry redirects forbidden") }}}, nil
}

func (c *Consumer) Run(ctx context.Context) {
	defer c.client.CloseIdleConnections()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		c.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Consumer) Refresh(ctx context.Context) {
	signals, err := c.fetch(ctx)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.state = "unavailable"
		c.signals = map[string]Signal{}
		return
	}
	// A replay cannot refresh old data. Per-observation timestamps are checked
	// again at selection time, independently of transport receipt time.
	for id, s := range signals {
		if old, ok := c.signals[id]; ok && s.ObservedAt.Before(old.ObservedAt) {
			c.state = "unavailable"
			c.signals = map[string]Signal{}
			return
		}
	}
	c.signals = signals
	c.receivedAt = now
	c.lastSuccess = now
	c.state = "ready"
	if len(signals) == 0 {
		c.state = "no_data"
	}
}

func (c *Consumer) fetch(ctx context.Context) (map[string]Signal, error) {
	token, e := ReadSecret(c.tokenFile)
	if e != nil || len(token) < 32 {
		return nil, errors.New("telemetry token unavailable")
	}
	req, e := http.NewRequestWithContext(ctx, "GET", c.endpoint, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, e := c.client.Do(req)
	if e != nil {
		return nil, errors.New("telemetry fetch failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("telemetry response unavailable")
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, 128*1024+1))
	if e != nil || len(data) > 128*1024 {
		return nil, errors.New("telemetry response too large")
	}
	var doc Document
	if json.Unmarshal(data, &doc) != nil || doc.Version != 1 || doc.Mode != "observe" || !doc.Enabled || len(doc.Routers) > 4 {
		return nil, errors.New("unsupported telemetry document")
	}
	result := map[string]Signal{}
	routers := map[string]bool{}
	now := time.Now()
	for _, r := range doc.Routers {
		if !slug.MatchString(r.RouterID) || routers[r.RouterID] || len(r.WANs) > 4 || len(r.Capabilities) > 16 {
			return nil, errors.New("ambiguous telemetry document")
		}
		routers[r.RouterID] = true
		if !r.Fresh || r.State != "ready" || r.ObservedAt.IsZero() || now.Sub(r.ObservedAt) > ConsumerMaxAge || r.ObservedAt.After(now.Add(time.Second)) {
			continue
		}
		counters, link := false, false
		for _, v := range r.Capabilities {
			if v == "wan.counters" {
				counters = true
			}
			if v == "wan.link_state" {
				link = true
			}
		}
		ids, interfaces := map[string]bool{}, map[string]bool{}
		for _, w := range r.WANs {
			if !slug.MatchString(w.ID) || ids[w.ID] || len(w.Interface) == 0 || len(w.Interface) > 128 || interfaces[w.Interface] {
				return nil, errors.New("ambiguous WAN observation")
			}
			ids[w.ID] = true
			interfaces[w.Interface] = true
			if link && w.Quality != "measured" && w.Quality != "baseline" && w.Quality != "counter_reset" && w.Quality != "link_down" {
				return nil, errors.New("unknown link observation quality")
			}
			if !slug.MatchString(w.ISP) {
				return nil, errors.New("invalid ISP identity")
			}
			if _, ok := result[w.ISP]; ok {
				return nil, errors.New("ambiguous ISP observation")
			}
			s := Signal{ISP: w.ISP, Router: r.RouterID, ObservedAt: r.ObservedAt, Fresh: true, LinkKnown: link, LinkDown: link && (!w.Running || w.Disabled)}
			if counters && w.Quality == "measured" && !s.LinkDown {
				for _, v := range []*float64{w.DownloadBPS, w.UploadBPS} {
					if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 || *v > 1.25e11) {
						return nil, errors.New("invalid WAN rate")
					}
				}
				s.DownloadBPS = w.DownloadBPS
				s.UploadBPS = w.UploadBPS
			}
			result[w.ISP] = s
		}
	}
	return result, nil
}

func (c *Consumer) Signal(isp string, now time.Time) (Signal, bool) {
	if c == nil {
		return Signal{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.signals[isp]
	if !ok || c.state != "ready" || now.Sub(c.receivedAt) > ConsumerMaxAge || now.Sub(s.ObservedAt) > ConsumerMaxAge || s.ObservedAt.After(now.Add(time.Second)) {
		return Signal{}, false
	}
	return s, true
}

func (c *Consumer) Status(now time.Time) ConsumerStatus {
	if c == nil {
		return ConsumerStatus{State: "disabled", Signals: []Signal{}}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := ConsumerStatus{Configured: true, State: c.state, LastSuccess: c.lastSuccess, Signals: []Signal{}}
	if c.state == "ready" && now.Sub(c.receivedAt) > ConsumerMaxAge {
		s.State = "stale"
	}
	for _, v := range c.signals {
		if now.Sub(v.ObservedAt) > ConsumerMaxAge || now.Sub(c.receivedAt) > ConsumerMaxAge {
			v.Fresh = false
		}
		s.Signals = append(s.Signals, v)
	}
	if s.State == "ready" {
		anyFresh := false
		for _, v := range s.Signals {
			anyFresh = anyFresh || v.Fresh
		}
		if !anyFresh {
			s.State = "stale"
		}
	}
	return s
}
