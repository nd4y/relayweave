package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"proxyplatform/internal/config"
)

type Instance struct {
	ProbeAllowed   func() bool
	probeConfig    atomic.Pointer[config.Config]
	probeRequested atomic.Bool
	probeMu        sync.Mutex
	Config         config.Core
	Routes         []config.Route
	Version        string
	SHA256         string
	PID            atomic.Int64
	Ready          atomic.Bool
	Restarts       atomic.Int64
	Starts         atomic.Int64
	LatencyUS      atomic.Int64
	ProbeSamples   atomic.Int64
	LastProbe      atomic.Int64
	HealthRequests atomic.Int64
}

func (i *Instance) ConfigureProbe(c config.Config) {
	i.configureProbe(c, false)
}

func (i *Instance) ConfigureValidatedProbe(c config.Config) {
	i.configureProbe(c, true)
}

func (i *Instance) configureProbe(c config.Config, validated bool) {
	i.probeMu.Lock()
	defer i.probeMu.Unlock()
	p := config.Config{ProbeURL: c.ProbeURL, ProbeFallbackURLs: append([]string(nil), c.ProbeFallbackURLs...), ProbeIntervalSeconds: c.ProbeIntervalSeconds, ConnectTimeoutMS: c.ConnectTimeoutMS}
	i.probeConfig.Store(&p)
	if validated {
		i.Ready.Store(true)
		i.LastProbe.Store(time.Now().Unix())
	}
}

// CheckRoute validates candidate settings without changing the accepted path's
// health or probe configuration. Cached Ready belongs to the previous revision.
func (i *Instance) CheckRoute(ctx context.Context, endpoint string, c config.Config) bool {
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for !localListenersReady([]config.Route{{Endpoint: endpoint}}) {
		select {
		case <-startup.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	if i.ProbeAllowed != nil && !i.ProbeAllowed() {
		return false
	}
	for _, target := range append([]string{c.ProbeURL}, c.ProbeFallbackURLs...) {
		if probe(ctx, i.Adapter(), endpoint, target, c.Timeout()) {
			return i.ProbeAllowed == nil || i.ProbeAllowed()
		}
	}
	return false
}

// Do not publish a result from settings that changed while network I/O ran.
func (i *Instance) publishProbe(settings *config.Config, ok bool, elapsed time.Duration) {
	if i.ProbeAllowed != nil && !i.ProbeAllowed() {
		ok = false
	}
	i.probeMu.Lock()
	defer i.probeMu.Unlock()
	if i.probeConfig.Load() != settings {
		return
	}
	i.Ready.Store(ok)
	i.LastProbe.Store(time.Now().Unix())
	if ok {
		old := i.LatencyUS.Load()
		v := elapsed.Microseconds()
		if old == 0 {
			i.LatencyUS.Store(v)
		} else {
			i.LatencyUS.Store((old*7 + v) / 8)
		}
		i.ProbeSamples.Add(1)
	}
}

// Passive failures request independent confirmation. They do not quarantine a
// proxy: a destination failure or a valid one-way stream may have no response.
// The one-second supervisor tick coalesces requests and bounds probe frequency.
func (i *Instance) RequestHealthCheck() {
	i.HealthRequests.Add(1)
	i.probeRequested.Store(true)
}

func (i *Instance) healthTimeout(c config.Config, confirmation bool) time.Duration {
	configured := c.Timeout()
	latency := time.Duration(i.LatencyUS.Load()) * time.Microsecond
	if !confirmation || !i.Ready.Load() || latency <= 0 {
		return configured
	}
	// Confirm a passive failure promptly using the path's successful RTT.
	// Slow paths get four RTTs plus jitter; new/unhealthy paths keep their
	// full recovery budget. A destination failure alone never changes Ready.
	return min(configured, max(time.Second, 4*latency+250*time.Millisecond))
}

type Manager struct {
	Instances map[string]*Instance
	wg        sync.WaitGroup
}

func New(c config.Config) (*Manager, error) {
	m := &Manager{Instances: map[string]*Instance{}}
	for _, v := range c.Cores {
		adapter, e := AdapterFor(v.Family)
		if e != nil {
			return nil, e
		}
		hash, e := FileHash(v.Binary)
		if e != nil {
			return nil, e
		}
		if v.SHA256 != "" && hash != v.SHA256 {
			return nil, fmt.Errorf("core_binary_digest_mismatch")
		}
		version, e := adapter.Version(context.Background(), v.Binary, v.Version)
		if e != nil {
			return nil, e
		}
		m.Instances[v.ID] = &Instance{Config: v, Version: version, SHA256: hash}
	}
	for _, r := range c.Routes {
		m.Instances[r.Core].Routes = append(m.Instances[r.Core].Routes, r)
	}
	return m, nil
}
func Render(routes []config.Route) ([]byte, error) {
	var ins, outs, rules []any
	for _, r := range routes {
		h, p, e := net.SplitHostPort(r.Endpoint)
		if e != nil {
			return nil, e
		}
		port, _ := strconv.Atoi(p)
		var outbound map[string]any
		if e = json.Unmarshal(r.Outbound, &outbound); e != nil {
			return nil, e
		}
		outbound["tag"] = "out-" + r.ID
		ins = append(ins, map[string]any{"tag": "in-" + r.ID, "listen": h, "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true, "ip": h}})
		outs = append(outs, outbound)
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{"in-" + r.ID}, "outboundTag": "out-" + r.ID})
	}
	// A missed rule must never fall through to a proxy or direct route.
	outs = append([]any{map[string]any{"tag": "blocked", "protocol": "blackhole", "settings": map[string]any{}}}, outs...)
	return json.MarshalIndent(map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": ins, "outbounds": outs, "routing": map[string]any{"domainStrategy": "AsIs", "rules": rules}}, "", "  ")
}
func (m *Manager) Start(ctx context.Context, c config.Config) error {
	if e := os.MkdirAll(c.StateDir, 0700); e != nil {
		return e
	}
	for _, i := range m.Instances {
		adapter := i.Adapter()
		for _, route := range i.Routes {
			if err := adapter.Validate(route); err != nil {
				return err
			}
		}
		data, e := adapter.RenderConfig(i.Routes)
		if e != nil {
			return e
		}
		path := filepath.Join(c.StateDir, i.Config.ID+".json")
		if e = os.WriteFile(path, data, 0600); e != nil {
			return e
		}
		test, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := exec.CommandContext(test, i.Config.Binary, adapter.TestArgs(path)...).Run()
		cancel()
		if err != nil {
			return fmt.Errorf("core %s rejected generated configuration: %w", i.Config.ID, err)
		}
	}
	for _, i := range m.Instances {
		m.wg.Add(1)
		go func(i *Instance) { defer m.wg.Done(); i.run(ctx, c) }(i)
	}
	return nil
}
func (m *Manager) Wait() { m.wg.Wait() }
func (i *Instance) run(ctx context.Context, c config.Config) {
	backoff := time.Second
	var crashes []time.Time
	for ctx.Err() == nil {
		if digest, e := FileHash(i.Config.Binary); e != nil || digest != i.SHA256 {
			i.Ready.Store(false)
			slog.Error("core binary changed; execution blocked", "core", i.Config.ID)
			return
		}
		if len(crashes) >= 5 {
			until := crashes[len(crashes)-5].Add(10 * time.Minute)
			if time.Now().Before(until) {
				slog.Error("core quarantined", "core", i.Config.ID)
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Until(until)):
				}
			}
		}
		if len(crashes) > 5 {
			crashes = crashes[len(crashes)-5:]
		}
		path := filepath.Join(c.StateDir, i.Config.ID+".json")
		cmd := exec.Command(i.Config.Binary, i.Adapter().RunArgs(path)...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		logger := &coreLog{core: i.Config.ID}
		cmd.Stdout = logger
		cmd.Stderr = logger
		started := time.Now()
		if e := cmd.Start(); e != nil {
			slog.Error("core start failed", "core", i.Config.ID, "error", e)
			crashes = append(crashes, time.Now())
		} else {
			i.Starts.Add(1)
			i.PID.Store(int64(cmd.Process.Pid))
			os.WriteFile(filepath.Join(c.StateDir, i.Config.ID+".pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
			slog.Info("core started", "core", i.Config.ID, "version", i.Config.Version, "pid", cmd.Process.Pid)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			ticker := time.NewTicker(time.Second)
			nextProbe := time.Time{}
			var lastSettings *config.Config
			privateReady := false
			waitingLogged := false
			running := true
			for running {
				select {
				case <-ctx.Done():
					i.Ready.Store(false)
					syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
						<-done
					}
					running = false
				case e := <-done:
					i.Ready.Store(false)
					slog.Warn("core exited", "core", i.Config.ID, "error", e)
					running = false
				case <-ticker.C:
					if i.ProbeAllowed != nil && !i.ProbeAllowed() {
						i.Ready.Store(false)
						nextProbe = time.Time{}
						continue
					}
					probeSettings := c
					settings := i.probeConfig.Load()
					if settings != nil {
						probeSettings = *settings
						if settings != lastSettings {
							nextProbe = time.Time{}
							lastSettings = settings
						}
					}
					confirmation := i.probeRequested.Swap(false)
					if confirmation {
						nextProbe = time.Time{}
					}
					if time.Now().Before(nextProbe) {
						continue
					}
					if !privateReady {
						privateReady = localListenersReady(i.Routes)
						if !privateReady {
							if !waitingLogged {
								slog.Info("waiting for private core listener", "core", i.Config.ID)
								waitingLogged = true
							}
							continue
						}
					}
					ok := true
					timeout := i.healthTimeout(probeSettings, confirmation)
					probeStarted := time.Now()
					for _, r := range i.Routes {
						passed := probe(ctx, i.Adapter(), r.Endpoint, probeSettings.ProbeURL, timeout)
						if !passed {
							for _, url := range probeSettings.ProbeFallbackURLs {
								if probe(ctx, i.Adapter(), r.Endpoint, url, timeout) {
									passed = true
									break
								}
							}
						}
						if !passed {
							ok = false
							break
						}
					}
					if i.LastProbe.Load() == 0 {
						slog.Info("initial core health completed", "core", i.Config.ID, "healthy", ok)
					}
					i.publishProbe(settings, ok, time.Since(probeStarted))
					if probeSettings.ProbeIntervalSeconds > 0 {
						nextProbe = time.Now().Add(time.Duration(probeSettings.ProbeIntervalSeconds) * time.Second)
					}
				}
			}
			ticker.Stop()
			i.PID.Store(0)
			i.Ready.Store(false)
			os.Remove(filepath.Join(c.StateDir, i.Config.ID+".pid"))
			if ctx.Err() != nil {
				return
			}
			crashes = append(crashes, time.Now())
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}
		i.Restarts.Add(1)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

var probeSlots = make(chan struct{}, 4)

// A process can exist before its SOCKS listeners finish starting. This is not
// a completed upstream health failure and must not prematurely reject a canary.
func localListenersReady(routes []config.Route) bool {
	for _, route := range routes {
		connection, err := net.DialTimeout("tcp", route.Endpoint, 100*time.Millisecond)
		if err != nil {
			return false
		}
		connection.Close()
	}
	return true
}

func probe(ctx context.Context, adapter Adapter, endpoint, url string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case probeSlots <- struct{}{}:
		defer func() { <-probeSlots }()
	case <-ctx.Done():
		return false
	}
	tr := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		return adapter.OpenTCP(ctx, endpoint, addr, timeout)
	}}
	defer tr.CloseIdleConnections()
	req, e := http.NewRequestWithContext(ctx, "GET", url, nil)
	if e != nil {
		return false
	}
	resp, e := (&http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if e != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// Core messages may contain subscription credentials and destination addresses. Export
// bounded, structured severity events, never the untrusted raw message.
type coreLog struct {
	mu   sync.Mutex
	core string
	last time.Time
}

func (l *coreLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := strings.ToLower(string(b))
	if time.Since(l.last) > time.Second && (strings.Contains(s, "error") || strings.Contains(s, "warning") || strings.Contains(s, "failed")) {
		slog.Warn("core diagnostic", "core", l.core, "detail", "redacted", "reason", coreDiagnosticReason(s))
		l.last = time.Now()
	}
	return len(b), nil
}

func coreDiagnosticReason(message string) string {
	switch {
	case strings.Contains(message, "address already in use"):
		return "listener_address_in_use"
	case strings.Contains(message, "no such file or directory"):
		return "configuration_file_missing"
	case strings.Contains(message, "permission denied"):
		return "permission_denied"
	default:
		return "redacted"
	}
}
