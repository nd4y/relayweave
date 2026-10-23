// Package routertelemetry is the vendor-neutral, read-only router integration.
// It is optional and has no dependency on worker routing or configuration apply.
package routertelemetry

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"
)

type Config struct {
	Version      int            `json:"version"`
	Enabled      bool           `json:"enabled"`
	Listen       string         `json:"listen"`
	TokenFile    string         `json:"token_file"`
	PollSeconds  int            `json:"poll_seconds"`
	StaleSeconds int            `json:"stale_after_seconds"`
	Routers      []RouterConfig `json:"routers"`
}

type RouterConfig struct {
	ID           string      `json:"id"`
	Driver       string      `json:"driver"`
	Enabled      bool        `json:"enabled"`
	Endpoint     string      `json:"endpoint"`
	ServerName   string      `json:"server_name"`
	CAFile       string      `json:"ca_file"`
	Username     string      `json:"username"`
	PasswordFile string      `json:"password_file"`
	WANs         []WANConfig `json:"wans"`
}

type WANConfig struct {
	ID        string `json:"id"`
	ISP       string `json:"isp"`
	Interface string `json:"interface"`
}

type Descriptor struct {
	ID           string   `json:"id"`
	Version      int      `json:"version"`
	Capabilities []string `json:"capabilities"`
}

// Adapter exposes observations only. There is deliberately no Execute, Apply,
// SetRoute, SetWeight, EnableWAN or arbitrary CLI/protocol passthrough method.
type Adapter interface {
	Read(context.Context) (Reading, error)
	Close() error
}

type Factory func(RouterConfig) (Adapter, error)
type Driver struct {
	Descriptor Descriptor
	New        Factory
}
type Registry map[string]Driver

type Reading struct {
	CPUPercent *float64 `json:"cpu_percent"`
	WANs       []WAN    `json:"wans"`
}

type WAN struct {
	ID        string `json:"id"`
	ISP       string `json:"isp"`
	Interface string `json:"interface"`
	Running   bool   `json:"running"`
	Disabled  bool   `json:"disabled"`
	// RX/TX is relative to the router WAN: RX=download, TX=upload.
	RXBytes     uint64   `json:"rx_bytes"`
	TXBytes     uint64   `json:"tx_bytes"`
	DownloadBPS *float64 `json:"download_bytes_per_second"`
	UploadBPS   *float64 `json:"upload_bytes_per_second"`
	Quality     string   `json:"quality"`
}

type Snapshot struct {
	RouterID      string    `json:"router_id"`
	Driver        string    `json:"driver"`
	Capabilities  []string  `json:"capabilities"`
	State         string    `json:"state"`
	Fresh         bool      `json:"fresh"`
	ObservedAt    time.Time `json:"observed_at"`
	LastAttemptAt time.Time `json:"last_attempt_at"`
	Reading
}

type Document struct {
	Version int        `json:"version"`
	Mode    string     `json:"mode"`
	Enabled bool       `json:"enabled"`
	Routers []Snapshot `json:"routers"`
}

var slug = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func Load(path string, registry Registry) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, errors.New("cannot open router telemetry config")
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 64<<10))
	dec.DisallowUnknownFields()
	var cfg Config
	if err = dec.Decode(&cfg); err != nil {
		return cfg, errors.New("invalid router telemetry config")
	}
	if dec.Decode(new(any)) != io.EOF {
		return cfg, errors.New("unexpected trailing config data")
	}
	if cfg.Version != 1 {
		return cfg, errors.New("unsupported router telemetry config version")
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:18530"
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return cfg, errors.New("telemetry listener must bind an explicit loopback IP")
	}
	if cfg.PollSeconds == 0 {
		cfg.PollSeconds = 2
	}
	if cfg.StaleSeconds == 0 {
		cfg.StaleSeconds = 10
	}
	if cfg.PollSeconds < 2 || cfg.PollSeconds > 60 || cfg.StaleSeconds < 3*cfg.PollSeconds || cfg.StaleSeconds > 300 {
		return cfg, errors.New("invalid polling/staleness budget")
	}
	if len(cfg.Routers) > 4 {
		return cfg, errors.New("at most four routers supported")
	}
	ids, isps := map[string]bool{}, map[string]bool{}
	for _, r := range cfg.Routers {
		if !slug.MatchString(r.ID) || ids[r.ID] {
			return cfg, errors.New("invalid or duplicate router ID")
		}
		ids[r.ID] = true
		if _, ok := registry[r.Driver]; !ok {
			return cfg, errors.New("unknown router driver")
		}
		if len(r.WANs) == 0 || len(r.WANs) > 4 {
			return cfg, errors.New("each router needs one to four WAN bindings")
		}
		wans, interfaces := map[string]bool{}, map[string]bool{}
		for _, w := range r.WANs {
			if !slug.MatchString(w.ID) || !slug.MatchString(w.ISP) || wans[w.ID] || interfaces[w.Interface] || isps[w.ISP] || len(w.Interface) == 0 || len(w.Interface) > 128 {
				return cfg, errors.New("ambiguous or invalid WAN/ISP binding")
			}
			wans[w.ID], interfaces[w.Interface], isps[w.ISP] = true, true, true
		}
	}
	return cfg, nil
}

// ReadSecret never emits file contents or underlying path errors in logs.
func ReadSecret(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open credential file")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) == 0 || len(b) > 4096 {
		return "", errors.New("invalid credential file")
	}
	// A single final newline is convenient for mounted Docker secrets.
	if b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
		if len(b) > 0 && b[len(b)-1] == '\r' {
			b = b[:len(b)-1]
		}
	}
	if len(b) == 0 {
		return "", errors.New("empty credential")
	}
	return string(b), nil
}

type Manager struct {
	cfg       Config
	registry  Registry
	mu        sync.RWMutex
	snapshots map[string]Snapshot
}

func New(cfg Config, registry Registry) *Manager {
	m := &Manager{cfg: cfg, registry: registry, snapshots: map[string]Snapshot{}}
	for _, r := range cfg.Routers {
		state := "disabled"
		if cfg.Enabled && r.Enabled {
			state = "starting"
		}
		m.snapshots[r.ID] = Snapshot{RouterID: r.ID, Driver: r.Driver, Capabilities: registry[r.Driver].Descriptor.Capabilities, State: state, Reading: Reading{WANs: []WAN{}}}
	}
	return m
}

func (m *Manager) Run(ctx context.Context) {
	var wg sync.WaitGroup
	if m.cfg.Enabled {
		for _, r := range m.cfg.Routers {
			if r.Enabled {
				wg.Add(1)
				go func(r RouterConfig) { defer wg.Done(); m.collect(ctx, r) }(r)
			}
		}
	}
	<-ctx.Done()
	wg.Wait()
}

func (m *Manager) collect(ctx context.Context, cfg RouterConfig) {
	// Failures remain isolated to this router and never reach the proxy runtime.
	a, err := m.registry[cfg.Driver].New(cfg)
	if err != nil {
		m.fail(cfg.ID, time.Now())
		return
	}
	defer a.Close()
	ticker := time.NewTicker(time.Duration(m.cfg.PollSeconds) * time.Second)
	defer ticker.Stop()
	var previous *Snapshot
	for {
		now := time.Now()
		pollCtx, cancel := context.WithTimeout(ctx, 1800*time.Millisecond)
		reading, err := a.Read(pollCtx)
		cancel()
		if err != nil {
			m.fail(cfg.ID, now)
			previous = nil
		} else {
			s := Snapshot{RouterID: cfg.ID, Driver: cfg.Driver, Capabilities: m.registry[cfg.Driver].Descriptor.Capabilities, State: "ready", Fresh: true, ObservedAt: now, LastAttemptAt: now, Reading: reading}
			applyRates(&s, previous, time.Duration(m.cfg.StaleSeconds)*time.Second)
			m.mu.Lock()
			m.snapshots[cfg.ID] = s
			m.mu.Unlock()
			previous = &s
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) fail(id string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.snapshots[id]
	s.State = "unavailable"
	s.Fresh = false
	s.LastAttemptAt = now
	// Keep the timestamp of the last success, but never expose old rates as live.
	s.Reading = Reading{WANs: []WAN{}}
	m.snapshots[id] = s
}

func applyRates(s, previous *Snapshot, maxGap time.Duration) {
	for i := range s.WANs {
		w := &s.WANs[i]
		w.Quality = "baseline"
		w.DownloadBPS = nil
		w.UploadBPS = nil
		if !w.Running || w.Disabled {
			w.Quality = "link_down"
			continue
		}
		if previous == nil {
			continue
		}
		dt := s.ObservedAt.Sub(previous.ObservedAt)
		if dt <= 0 || dt > maxGap {
			continue
		}
		for _, p := range previous.WANs {
			if p.ID == w.ID && p.Interface == w.Interface && p.Running && !p.Disabled {
				if w.RXBytes < p.RXBytes || w.TXBytes < p.TXBytes {
					w.Quality = "counter_reset"
					break
				}
				rx := float64(w.RXBytes-p.RXBytes) / dt.Seconds()
				tx := float64(w.TXBytes-p.TXBytes) / dt.Seconds()
				w.DownloadBPS = &rx
				w.UploadBPS = &tx
				w.Quality = "measured"
				break
			}
		}
	}
}

func (m *Manager) Document(now time.Time) Document {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d := Document{Version: 1, Mode: "observe", Enabled: m.cfg.Enabled, Routers: []Snapshot{}}
	for _, r := range m.cfg.Routers {
		s := m.snapshots[r.ID]
		if s.Fresh && now.Sub(s.ObservedAt) > time.Duration(m.cfg.StaleSeconds)*time.Second {
			s.State = "stale"
			s.Fresh = false
			s.Reading = Reading{WANs: []WAN{}}
		}
		d.Routers = append(d.Routers, s)
	}
	return d
}

func (m *Manager) Handler(token string) http.Handler {
	expected := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != "GET" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/livez" {
			w.WriteHeader(200)
			return
		}
		actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		d := m.Document(time.Now())
		switch r.URL.Path {
		case "/v1/snapshot":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(d)
		case "/readyz":
			ready := false
			for _, s := range d.Routers {
				if s.State == "disabled" {
					continue
				}
				ready = true
				if !s.Fresh {
					w.WriteHeader(503)
					return
				}
			}
			if !ready {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(200)
		case "/metrics":
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			fmt.Fprintln(w, "# HELP relayweave_router_fresh Whether router telemetry is currently fresh.\n# TYPE relayweave_router_fresh gauge")
			for _, s := range d.Routers {
				fresh := 0
				if s.Fresh {
					fresh = 1
				}
				fmt.Fprintf(w, "relayweave_router_fresh{router=%q} %d\n", s.RouterID, fresh)
				if !s.Fresh {
					continue
				}
				if s.CPUPercent != nil {
					fmt.Fprintf(w, "relayweave_router_cpu_percent{router=%q} %g\n", s.RouterID, *s.CPUPercent)
				}
				for _, wan := range s.WANs {
					fmt.Fprintf(w, "relayweave_router_wan_received_bytes_total{router=%q,wan=%q,isp=%q} %d\n", s.RouterID, wan.ID, wan.ISP, wan.RXBytes)
					fmt.Fprintf(w, "relayweave_router_wan_sent_bytes_total{router=%q,wan=%q,isp=%q} %d\n", s.RouterID, wan.ID, wan.ISP, wan.TXBytes)
					if wan.DownloadBPS != nil {
						fmt.Fprintf(w, "relayweave_router_wan_download_bytes_per_second{router=%q,wan=%q,isp=%q} %g\n", s.RouterID, wan.ID, wan.ISP, *wan.DownloadBPS)
					}
					if wan.UploadBPS != nil {
						fmt.Fprintf(w, "relayweave_router_wan_upload_bytes_per_second{router=%q,wan=%q,isp=%q} %g\n", s.RouterID, wan.ID, wan.ISP, *wan.UploadBPS)
					}
				}
			}
		default:
			w.WriteHeader(404)
		}
	})
}
