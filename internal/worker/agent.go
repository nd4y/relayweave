package worker

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/identity"
	"proxyplatform/internal/store"
	"proxyplatform/internal/topology"
	"proxyplatform/internal/wire"
)

type savedState struct {
	Rollovers       []rolloverRecord       `json:"rollovers,omitempty"`
	ProbeDay        time.Time              `json:"probe_day"`
	ProbeBudget     map[string]int64       `json:"probe_budget"`
	FenceGeneration int64                  `json:"fence_generation"`
	GuardVersion    int64                  `json:"guard_version,omitempty"`
	Constraints     *config.Config         `json:"constraints,omitempty"`
	Generation      int64                  `json:"generation"`
	Config          *config.Config         `json:"config,omitempty"`
	Artifacts       []core.Artifact        `json:"artifacts"`
	Results         []wire.Result          `json:"results"`
	Done            map[string]wire.Result `json:"done"`
	Drained         bool                   `json:"drained"`
}
type Agent struct {
	retryAt               atomic.Int64
	failureAttempts       atomic.Int64
	workerID              string
	persisted             bool
	persistedGeneration   int64
	persistedFence        int64
	persistedGuardVersion int64
	loadedGuard           *config.LiveGuard
	ctx                   context.Context
	dir                   string
	mu                    sync.Mutex
	artifactMu            sync.Mutex
	state                 savedState
	aead                  cipher.AEAD
	runtime               *Runtime
	gateway               *Gateway
	failed                atomic.Int64
	applying              atomic.Int64
	commands              chan wire.Command
	inflight              map[string]bool
}

func AgentRun(ctx context.Context) error {
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "/data"
	}
	idDir := os.Getenv("IDENTITY_DIR")
	if idDir == "" {
		idDir = "/worker-identity"
	}
	id := os.Getenv("WORKER_ID")
	if id == "" {
		id = "worker-1"
	}
	metricsTarget, e := config.MetricsTarget(id, os.Getenv("METRICS_TARGET"))
	if e != nil {
		return e
	}
	address := os.Getenv("CONTROL_ADDRESS")
	if address == "" {
		address = "control-plane:9443"
	}
	a := &Agent{ctx: ctx, dir: dir, commands: make(chan wire.Command, 16), inflight: map[string]bool{}}
	a.workerID = id
	if e := a.load(); e != nil {
		return e
	}
	root := filepath.Join(dir, "runtime")
	if entries, e := os.ReadDir(root); e == nil {
		for _, v := range entries {
			if v.IsDir() && regexp.MustCompile(`^[a-zA-Z0-9_-]{1,48}-[a-f0-9]{16}(?:-[a-f0-9]{8})?$`).MatchString(v.Name()) {
				os.RemoveAll(filepath.Join(root, v.Name()))
			}
		}
	}
	r := NewRuntime(ctx, root)
	r.restoreRollovers = a.state.Rollovers
	if !a.state.ProbeDay.IsZero() {
		r.mu.Lock()
		r.probeWindow = a.state.ProbeDay
		r.budget = a.state.ProbeBudget
		if r.budget == nil {
			r.budget = map[string]int64{}
		}
		r.mu.Unlock()
	}
	a.runtime = r
	if a.loadedGuard != nil {
		r.RestrictLiveGuard(*a.loadedGuard)
	}
	g := New(ctx, config.Config{}, nil)
	g.Dynamic = r
	a.gateway = g
	r.mu.Lock()
	r.config = config.Config{MetricsListen: config.MetricsAddress(), MaxConnections: 5000, ConnectTimeoutMS: 3000}
	r.mu.Unlock()
	if e := g.startMetrics(); e != nil {
		return e
	}
	defer g.Close()
	if a.state.Config != nil {
		c := *a.state.Config
		c.Generation = a.state.Generation
		r.drained.Store(a.state.Drained)
		if e := config.ValidateDeploymentBinds(c); e != nil {
			slog.Warn("cached configuration incompatible with deployment bind policy; waiting for fresh assignment")
			a.state.Generation = 0
			a.state.Config = nil
		} else if e := pinOffline(&c); e != nil {
			slog.Error("last known good artifact verification failed; new flows blocked")
			a.state.Generation = 0
			a.state.Config = nil
		} else if e := r.Restore(c, g); e != nil {
			return e
		} else {
			slog.Info("last known good configuration restored", "generation", a.state.Generation)
		}
	}
	if a.state.Constraints != nil {
		r.Restrict(*a.state.Constraints)
	}
	tlsConfig, e := identity.ClientTLS(idDir)
	if e != nil {
		return e
	}
	conn, e := newControlConnection(address, tlsConfig)
	if e != nil {
		return e
	}
	defer conn.Close()
	admissionDone := make(chan struct{})
	go func() {
		defer close(admissionDone)
		a.admissionLoop(ctx, func(call context.Context, request *wire.AdmissionRequest) (*wire.AdmissionResponse, error) {
			return wire.Admission(call, conn, request)
		}, time.Second)
	}()
	// Keep the process and connection alive until the last received restriction
	// has finished its orderly-shutdown flush.
	defer func() { <-admissionDone }()
	go a.commandLoop()
	go a.adaptiveProbes()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastLeaseWrite := time.Time{}
	online := false
	var pendingTopology *topology.Frame
	for {
		a.mu.Lock()
		generation := a.state.Generation
		guardVersion := a.persistedGuardVersion
		results := append([]wire.Result(nil), a.state.Results...)
		if len(results) > 100 {
			results = results[:100]
		}
		a.mu.Unlock()
		status := r.Status(g)
		status["failed_generation"] = a.failed.Load()
		status["applying_generation"] = a.applying.Load()
		status["metrics_target"] = metricsTarget
		status["live_guard_version"] = guardVersion
		b, _ := json.Marshal(status)
		csr, _ := identity.RenewalCSR(idDir)
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		if pendingTopology == nil {
			pendingTopology = r.TopologyFrame(g)
		}
		assignment, err := wire.Sync(call, conn, &wire.Hello{RoutingVersion: 2, Topology: pendingTopology, WorkerID: id, Generation: generation, GuardVersion: guardVersion, Status: b, Results: results, CSR: csr})
		cancel()
		if err != nil {
			if online {
				slog.Warn("control connection unavailable; using local lease")
				online = false
			}
		} else {
			pendingTopology = nil
			if !online {
				slog.Info("control connection established")
				online = true
			}
			r.RenewLease(assignment.LeaseUntil)
			guardOK := true
			if assignment.LiveGuard != nil {
				if err := a.acceptLiveGuard(*assignment.LiveGuard); err != nil {
					guardOK = false
					slog.Error("live admission fence could not be persisted")
				}
			}
			if len(assignment.Certificate) > 0 {
				if err := identity.AcceptRenewal(idDir, assignment.Certificate); err != nil {
					slog.Error("client certificate renewal rejected")
				}
			}
			a.mu.Lock()
			if len(results) > 0 && len(a.state.Results) >= len(results) {
				a.state.Results = a.state.Results[len(results):]
			}
			if a.state.Config != nil {
				a.state.Config.LeaseUntil = assignment.LeaseUntil
			}
			if time.Since(lastLeaseWrite) > time.Minute || len(results) > 0 || a.runtime.rolloverDirty.Load() {
				a.saveLocked()
				lastLeaseWrite = time.Now()
			}
			a.mu.Unlock()
			if guardOK && assignment.Config != nil && assignment.Generation > generation && a.mayRetryConfiguration(assignment.Generation, time.Now()) && a.applying.CompareAndSwap(0, assignment.Generation) {
				go a.apply(*assignment)
			}
			for _, command := range assignment.Commands {
				a.mu.Lock()
				// Done contains a durable interruption marker while execution is in
				// progress. Only replay it after a restart, never during this run.
				if a.inflight[command.ID] {
					a.mu.Unlock()
					continue
				}
				done, exists := a.state.Done[command.ID]
				if exists {
					a.state.Results = append(a.state.Results, done)
					a.saveLocked()
					a.mu.Unlock()
					continue
				}
				a.inflight[command.ID] = true
				a.mu.Unlock()
				select {
				case a.commands <- command:
				default:
					a.mu.Lock()
					delete(a.inflight, command.ID)
					a.mu.Unlock()
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (a *Agent) apply(assignment wire.Assignment) {
	defer a.applying.Store(0)
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	defer cancel()
	c := *assignment.Config
	c.Generation = assignment.Generation
	if host := os.Getenv("UDP_ADVERTISE"); host != "" {
		c.UDP.Advertise = host
	}
	if e := c.Validate(); e != nil {
		a.configurationFailed(assignment.Generation)
		return
	}
	a.mu.Lock()
	// The Sync request may have captured an older generation before an apply
	// completed. Recheck durable state instead of repeating its I/O and canary.
	if assignment.Generation <= a.state.Generation {
		a.mu.Unlock()
		return
	}
	if assignment.Generation < a.state.FenceGeneration {
		a.mu.Unlock()
		return
	}
	a.state.FenceGeneration = assignment.Generation
	a.state.Constraints = &c
	// Restrict new traffic immediately; slow durable writes must not leave old
	// permissions active. The generation is acknowledged only after persistence.
	a.runtime.Restrict(c)
	slog.Info("revocation fence installed", "generation", assignment.Generation)
	persistErr := a.saveLocked()
	a.mu.Unlock()
	if persistErr != nil {
		a.configurationFailed(assignment.Generation)
		slog.Error("revocation state could not be persisted")
		return
	}
	if e := a.install(ctx, c, assignment.Artifacts); e != nil {
		a.configurationFailed(assignment.Generation)
		slog.Error("configuration artifact preparation failed", "generation", assignment.Generation, "reason", core.ArtifactFailureCode(e))
		return
	}
	if e := a.runtime.Apply(ctx, c, a.gateway); e != nil {
		a.configurationFailed(assignment.Generation)
		slog.Error("configuration rejected; previous revision retained", "generation", assignment.Generation, "reason", e.Error())
		return
	}
	a.mu.Lock()
	oldGeneration, oldConfig, oldArtifacts := a.state.Generation, a.state.Config, a.state.Artifacts
	a.state.Generation = assignment.Generation
	a.state.Config = &c
	a.state.Artifacts = assignment.Artifacts
	e := a.saveLocked()
	if e != nil {
		a.state.Generation, a.state.Config, a.state.Artifacts = oldGeneration, oldConfig, oldArtifacts
	}
	a.mu.Unlock()
	if e != nil {
		if oldConfig != nil {
			a.runtime.Apply(ctx, *oldConfig, a.gateway)
		} else {
			a.runtime.drained.Store(true)
		}
		slog.Error("last known good persistence failed")
		a.configurationFailed(assignment.Generation)
		return
	}
	a.failed.Store(0)
	slog.Info("configuration active", "generation", assignment.Generation, "routes", len(c.Routes))
}
func (a *Agent) install(ctx context.Context, c config.Config, artifacts []core.Artifact) error {
	a.artifactMu.Lock()
	defer a.artifactMu.Unlock()
	b, _ := json.Marshal(map[string]any{"artifacts": artifacts})
	path := filepath.Join(a.dir, "approved-artifacts.json")
	if e := store.AtomicFile(path, b, 0600); e != nil {
		return e
	}
	for i, v := range c.Cores {
		selector := v.Artifact
		if selector == "" {
			selector = v.Version
		}
		expected := ""
		for _, a := range artifacts {
			if a.ID() == selector && a.Arch == runtime.GOARCH {
				expected = a.BinarySHA256
				break
			}
		}
		if len(expected) != 64 {
			return fmt.Errorf("artifact_binary_digest_missing")
		}
		if v.Binary != "/opt/xray/builtin/xray" {
			if e := core.Install(ctx, path, selector, "/cores"); e != nil {
				return e
			}
		}
		actual, e := core.FileHash(v.Binary)
		if e != nil || actual != expected {
			return fmt.Errorf("artifact_binary_digest_mismatch")
		}
		c.Cores[i].SHA256 = expected
	}
	return nil
}

func pinOffline(c *config.Config) error {
	var lock struct {
		Artifacts []core.Artifact `json:"artifacts"`
	}
	if b, e := os.ReadFile("/app/artifacts.lock.json"); e == nil {
		json.Unmarshal(b, &lock)
	}
	for i, v := range c.Cores {
		expected := v.SHA256
		if expected == "" {
			for _, a := range lock.Artifacts {
				if a.Version == v.Version && a.Arch == runtime.GOARCH {
					expected = a.BinarySHA256
					break
				}
			}
		}
		if len(expected) != 64 {
			// A legacy unpinned engine remains blocked without disabling unrelated paths.
			expected = strings.Repeat("0", 64)
		}
		c.Cores[i].SHA256 = expected
	}
	return nil
}
func (a *Agent) load() error {
	if e := os.MkdirAll(a.dir, 0700); e != nil {
		return e
	}
	path := filepath.Join(a.dir, "worker.key")
	key, e := readBounded(path, 32)
	if os.IsNotExist(e) {
		for _, name := range []string{"last-good.enc", "previous-good.enc", "worker-meta.enc", "live-guard.enc"} {
			if _, err := os.Stat(filepath.Join(a.dir, name)); err == nil {
				return fmt.Errorf("worker_key_missing")
			}
		}
		key = make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return e
		}
		if e = store.AtomicFile(path, key, 0600); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return e
	}
	a.aead, e = cipher.NewGCM(block)
	if e != nil {
		return e
	}
	a.state = savedState{Done: map[string]wire.Result{}}
	for _, name := range []string{"last-good.enc", "previous-good.enc"} {
		b, err := readBounded(filepath.Join(a.dir, name), 64<<20)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		n := a.aead.NonceSize()
		if len(b) <= n {
			continue
		}
		plain, err := a.aead.Open(nil, b[:n], b[n:], []byte("relayweave-worker-v1"))
		if err != nil {
			continue
		}
		if json.Unmarshal(plain, &a.state) != nil {
			continue
		}
		if a.state.Done == nil {
			a.state.Done = map[string]wire.Result{}
		}
		a.persisted = true
		a.persistedGeneration = a.state.Generation
		a.persistedFence = a.state.FenceGeneration
		return a.loadJournals()
	}
	if _, e = os.Stat(filepath.Join(a.dir, "last-good.enc")); e == nil {
		return fmt.Errorf("last_known_good_corrupt")
	}
	return a.loadJournals()
}
func (a *Agent) saveLocked() error {
	if a.persisted && a.persistedGeneration == a.state.Generation && a.persistedFence == a.state.FenceGeneration {
		return a.saveMeta()
	}
	b, e := json.Marshal(a.state)
	if e != nil {
		return e
	}
	nonce := make([]byte, a.aead.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	sealed := a.aead.Seal(nonce, nonce, b, []byte("relayweave-worker-v1"))
	path := filepath.Join(a.dir, "last-good.enc")
	if old, e := readBounded(path, 64<<20); e == nil {
		n := a.aead.NonceSize()
		if len(old) > n {
			if _, err := a.aead.Open(nil, old[:n], old[n:], []byte("relayweave-worker-v1")); err == nil {
				if e = store.AtomicFile(filepath.Join(a.dir, "previous-good.enc"), old, 0600); e != nil {
					return e
				}
			}
		}
	}
	if e = store.AtomicFile(path, sealed, 0600); e != nil {
		return e
	}
	a.persisted = true
	a.persistedGeneration = a.state.Generation
	a.persistedFence = a.state.FenceGeneration
	return a.saveMeta()
}

type mutableState struct {
	Rollovers    []rolloverRecord       `json:"rollovers,omitempty"`
	GuardVersion int64                  `json:"guard_version,omitempty"`
	ProbeDay     time.Time              `json:"probe_day"`
	ProbeBudget  map[string]int64       `json:"probe_budget"`
	Generation   int64                  `json:"generation"`
	Fence        int64                  `json:"fence"`
	Lease        time.Time              `json:"lease"`
	Done         map[string]wire.Result `json:"done"`
	Results      []wire.Result          `json:"results"`
	Drained      bool                   `json:"drained"`
}

func (a *Agent) saveMeta() error {
	m := mutableState{Generation: a.state.Generation, Fence: a.state.FenceGeneration, GuardVersion: a.state.GuardVersion, Done: a.state.Done, Results: a.state.Results, Drained: a.state.Drained}
	m.ProbeDay, m.ProbeBudget = a.state.ProbeDay, a.state.ProbeBudget
	if a.runtime != nil {
		a.runtime.mu.RLock()
		m.Rollovers = a.runtime.rolloverRecordsLocked()
		m.ProbeDay = a.runtime.probeWindow
		m.ProbeBudget = map[string]int64{}
		for k, v := range a.runtime.budget {
			m.ProbeBudget[k] = v
		}
		a.runtime.mu.RUnlock()
	}
	if a.state.Config != nil {
		m.Lease = a.state.Config.LeaseUntil
	}
	plain, e := json.Marshal(m)
	if e != nil {
		return e
	}
	nonce := make([]byte, a.aead.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	if e = store.AtomicFile(filepath.Join(a.dir, "worker-meta.enc"), a.aead.Seal(nonce, nonce, plain, []byte("relayweave-meta-v1")), 0600); e != nil {
		return e
	}
	if a.runtime != nil {
		a.runtime.acknowledgeRollovers(m.Rollovers)
	}
	return nil
}
func (a *Agent) loadMeta() error {
	b, e := readBounded(filepath.Join(a.dir, "worker-meta.enc"), 2<<20)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	n := a.aead.NonceSize()
	if len(b) <= n || len(b) > 2<<20 {
		return fmt.Errorf("worker_metadata_corrupt")
	}
	plain, e := a.aead.Open(nil, b[:n], b[n:], []byte("relayweave-meta-v1"))
	if e != nil {
		return fmt.Errorf("worker_metadata_corrupt")
	}
	var m mutableState
	if json.Unmarshal(plain, &m) != nil || len(m.Done) > 1000 || len(m.Results) > 200 {
		return fmt.Errorf("worker_metadata_corrupt")
	}
	a.state.GuardVersion = max(a.state.GuardVersion, m.GuardVersion)
	if m.Generation < a.state.Generation || m.Fence < a.state.FenceGeneration {
		return nil
	}
	a.state.ProbeDay, a.state.ProbeBudget = m.ProbeDay, m.ProbeBudget
	if m.Done != nil {
		a.state.Done = m.Done
	}
	a.state.Results = m.Results
	if len(m.Rollovers) > processLimit {
		return fmt.Errorf("worker_metadata_corrupt")
	}
	a.state.Rollovers = m.Rollovers
	a.state.Drained = m.Drained
	if m.Fence > a.state.FenceGeneration || m.Generation > a.state.Generation {
		a.state.FenceGeneration = max(m.Fence, a.state.FenceGeneration)
		a.state.Constraints = &config.Config{Managed: true}
		return nil
	}
	if a.state.Config != nil && m.Generation == a.state.Generation && m.Fence == a.state.FenceGeneration {
		a.state.Config.LeaseUntil = m.Lease
	}
	return nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("worker_state_too_large")
	}
	return b, nil
}
func (a *Agent) commandLoop() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case command := <-a.commands:
			a.mu.Lock()
			if len(a.state.Done) >= 950 {
				pending := map[string]bool{}
				for _, r := range a.state.Results {
					pending[r.ID] = true
				}
				for id := range a.state.Done {
					if !pending[id] && !a.inflight[id] {
						delete(a.state.Done, id)
						if len(a.state.Done) < 800 {
							break
						}
					}
				}
			}
			a.state.Done[command.ID] = wire.Result{ID: command.ID, Error: "interrupted_before_completion"}
			e := a.saveLocked()
			a.mu.Unlock()
			result := wire.Result{ID: command.ID}
			if e != nil {
				result.Error = "command_persistence_failed"
			} else {
				ctx, cancel := context.WithTimeout(a.ctx, 150*time.Second)
				data, err := a.executeCommand(ctx, command)
				cancel()
				if err != nil {
					result.Error = err.Error()
				} else {
					result.Success = true
					result.Data, _ = json.Marshal(data)
				}
			}
			a.mu.Lock()
			a.state.Done[command.ID] = result
			a.state.Results = append(a.state.Results, result)
			delete(a.inflight, command.ID)
			a.saveLocked()
			a.mu.Unlock()
		}
	}
}
func (a *Agent) executeCommand(ctx context.Context, c wire.Command) (any, error) {
	if c.WorkerID != "" && c.WorkerID != a.workerID {
		return nil, fmt.Errorf("command_wrong_worker")
	}
	switch c.Action {
	case "drain":
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		json.Unmarshal(c.Payload, &body)
		enabled := true
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		a.runtime.drained.Store(enabled)
		a.mu.Lock()
		a.state.Drained = enabled
		a.mu.Unlock()
		return map[string]bool{"drained": enabled}, nil
	case "restart-core":
		var body struct {
			RouteID string `json:"route_id"`
		}
		json.Unmarshal(c.Payload, &body)
		if err := a.runtime.Restart(body.RouteID); err != nil {
			return nil, err
		}
		return map[string]bool{"restart_requested": true}, nil
	case "install-core":
		a.artifactMu.Lock()
		defer a.artifactMu.Unlock()
		var artifact core.Artifact
		if json.Unmarshal(c.Payload, &artifact) != nil {
			return nil, fmt.Errorf("artifact_invalid")
		}
		b, _ := json.Marshal(map[string]any{"artifacts": []core.Artifact{artifact}})
		path := filepath.Join(a.dir, "install-command.json")
		if err := store.AtomicFile(path, b, 0600); err != nil {
			return nil, fmt.Errorf("artifact_write_failed")
		}
		if err := core.Install(ctx, path, artifact.ID(), "/cores"); err != nil {
			return nil, fmt.Errorf("artifact_install_failed")
		}
		return map[string]string{"version": artifact.Version, "status": "verified"}, nil
	case "core-cache", "remove-core":
		return a.cacheCommand(ctx, c.Action, c.Payload)
	case "test-profile", "speed-test":
		return a.test(ctx, c)
	default:
		return nil, fmt.Errorf("unknown_worker_command")
	}
}
func (r *Runtime) Restore(c config.Config, g *Gateway) error {
	if err := c.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	r.config = c
	r.policies = map[string]*policyState{}
	for _, p := range c.Policies {
		r.policies[p.ID] = newPolicyState(p)
	}
	for _, v := range c.Routes {
		r.valid[v.ID] = v.ValidUntil
	}
	r.lease.Store(c.LeaseUntil.Unix())
	r.mu.Unlock()
	return g.Reconfigure(c)
}
