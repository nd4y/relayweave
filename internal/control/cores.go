package control

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"proxyplatform/internal/catalog"
	"proxyplatform/internal/core"
	"proxyplatform/internal/store"
	"proxyplatform/internal/wire"
)

var coreVersion = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}\.[0-9]{1,2}$`)
var coreSelector = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}\.[0-9]{1,2}(-[a-z][a-z0-9-]{0,23})?$`)

func (s *Server) approvedArtifacts(ctx context.Context) ([]core.Artifact, error) {
	var lock struct {
		Artifacts []core.Artifact `json:"artifacts"`
	}
	b, e := os.ReadFile("/app/artifacts.lock.json")
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(b, &lock); e != nil {
		return nil, e
	}
	objects, _, e := s.Store.List(ctx, "core-artifacts", 1000, 0)
	if e != nil {
		return nil, e
	}
	out := []core.Artifact{}
	seen := map[string]bool{}
	for _, a := range lock.Artifacts {
		out = append(out, a)
		seen[a.ID()+"/"+a.Arch] = true
	}
	for _, o := range objects {
		a, e := catalog.Decode[core.Artifact](o.Data)
		if e == nil && !seen[a.ID()+"/"+a.Arch] {
			out = append(out, a)
			seen[a.ID()+"/"+a.Arch] = true
		}
	}
	return out, nil
}
func (s *Server) cores(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 2 && (parts[1] == "cache" || parts[1] == "remove") && r.Method == "POST" {
		var body struct {
			WorkerID string `json:"worker_id"`
			Version  string `json:"version"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.WorkerID == "" {
			body.WorkerID = "worker-1"
		}
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,48}$`).MatchString(body.WorkerID) {
			fail(w, 400, "invalid_worker_id")
			return
		}
		if parts[1] == "remove" && !coreSelector.MatchString(body.Version) {
			fail(w, 400, "invalid_core_version")
			return
		}
		payload, _ := json.Marshal(body)
		action := "core-cache"
		if parts[1] == "remove" {
			action = "remove-core"
		}
		op, e := s.enqueue(r.Context(), action, body.WorkerID, payload, r.Header.Get("Idempotency-Key"))
		if e != nil {
			fail(w, 503, "operation_enqueue_failed")
			return
		}
		respond(w, 202, s.redact(op))
		return
	}
	if len(parts) == 2 && parts[1] == "approve" && r.Method == "POST" {
		var a core.Artifact
		if !decode(w, r, &a) {
			return
		}
		if err := validateCustomArtifact(a); err != nil {
			fail(w, 400, safeError(err))
			return
		}
		a.CustomApproved = true
		payload, _ := json.Marshal(a)
		op, err := s.enqueue(r.Context(), "approve-core", a.ID(), payload, r.Header.Get("Idempotency-Key"))
		if err != nil {
			fail(w, 503, "operation_enqueue_failed")
			return
		}
		respond(w, 202, s.redact(op))
		return
	}
	if len(parts) == 1 && r.Method == "GET" {
		items, e := s.approvedArtifacts(r.Context())
		if e != nil {
			fail(w, 503, "artifact_catalog_unavailable")
			return
		}
		local := []core.Artifact{}
		for _, a := range items {
			if a.Arch == runtime.GOARCH {
				local = append(local, a)
			}
		}
		items = local
		respond(w, 200, map[string]any{"items": items, "builtin": "26.10.17", "family": "xray", "architecture": runtime.GOARCH, "signature_policy": "official HTTPS + pinned SHA256; upstream signatures not assumed"})
		return
	}
	if len(parts) == 2 && parts[1] == "install" && r.Method == "POST" {
		var body struct {
			Version string `json:"version"`
		}
		if !decode(w, r, &body) {
			return
		}
		if !coreSelector.MatchString(body.Version) {
			fail(w, 400, "invalid_core_version")
			return
		}
		payload, _ := json.Marshal(body)
		op, e := s.enqueue(r.Context(), "install-core", body.Version, payload, r.Header.Get("Idempotency-Key"))
		if e != nil {
			fail(w, 503, "operation_enqueue_failed")
			return
		}
		respond(w, 202, s.redact(op))
		return
	}
	fail(w, 405, "method_not_allowed")
}
func (s *Server) installArtifact(ctx context.Context, op Operation) (any, error) {
	if !coreSelector.MatchString(op.Target) {
		return nil, errCode("invalid_core_version")
	}
	approved, e := s.approvedArtifacts(ctx)
	if e != nil {
		return nil, e
	}
	for _, a := range approved {
		if a.ID() == op.Target && a.Arch == runtime.GOARCH && len(a.BinarySHA256) == 64 {
			return s.queueArtifactInstall(ctx, a)
		}
	}
	archive := "Xray-linux-64.zip"
	if !coreVersion.MatchString(op.Target) {
		return nil, errCode("core_variant_not_approved")
	}
	if runtime.GOARCH == "arm64" {
		archive = "Xray-linux-arm64-v8a.zip"
	} else if runtime.GOARCH != "amd64" {
		return nil, errCode("architecture_not_supported")
	}
	a := core.Artifact{Version: op.Target, Arch: runtime.GOARCH, URL: "https://github.com/XTLS/Xray-core/releases/download/v" + op.Target + "/" + archive}
	request, e := http.NewRequestWithContext(ctx, "GET", a.URL+".dgst", nil)
	if e != nil {
		return nil, e
	}
	client := &http.Client{Timeout: 30 * time.Second}
	response, e := client.Do(request)
	if e != nil {
		return nil, errCode("artifact_manifest_download_failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errCode("artifact_release_not_found")
	}
	b, e := io.ReadAll(io.LimitReader(response.Body, 8193))
	if e != nil || len(b) > 8192 {
		return nil, errCode("artifact_manifest_invalid")
	}
	for _, line := range strings.Split(string(b), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok && strings.TrimSpace(key) == "SHA2-256" {
			a.SHA256 = strings.TrimSpace(value)
		}
	}
	if len(a.SHA256) != 64 {
		return nil, errCode("artifact_manifest_invalid")
	}
	if _, e = hex.DecodeString(a.SHA256); e != nil {
		return nil, errCode("artifact_manifest_invalid")
	}
	if a.BinarySHA256, e = s.inspectArtifact(ctx, a); e != nil {
		return nil, e
	}
	return s.queueArtifactInstall(ctx, a)
}

func (s *Server) queueArtifactInstall(ctx context.Context, a core.Artifact) (any, error) {
	data, _ := json.Marshal(a)
	id := "xray-" + a.ID() + "-" + a.Arch
	old, e := s.Store.Get(ctx, "core-artifacts", id)
	expected := int64(0)
	if e == nil {
		expected = old.Version
	}
	if e == nil {
		var existing core.Artifact
		json.Unmarshal(old.Data, &existing)
		if existing.SHA256 != a.SHA256 || existing.BinarySHA256 != a.BinarySHA256 {
			return nil, errCode("artifact_identity_immutable")
		}
	}
	if _, e = s.Store.Put(ctx, store.Object{Kind: "core-artifacts", ID: id, Name: "Xray " + a.ID(), Data: data}, expected); e != nil {
		return nil, e
	}
	commandPayload, _ := json.Marshal(a)
	if a.Arch != runtime.GOARCH {
		return map[string]string{"status": "approved_for_target_architecture", "artifact": a.ID()}, nil
	}
	return s.workerOperation(ctx, Operation{Action: "install-core", Target: a.ID(), Payload: commandPayload}, "install-"+a.ID()+"-"+store.NewID())
}
func (s *Server) workerOperation(ctx context.Context, op Operation, id string) (any, error) {
	command := wire.Command{ID: id, Action: op.Action, Target: op.Target, Payload: op.Payload}
	command.WorkerID = "worker-1"
	if op.Action == "drain" || op.Action == "restart-core" || op.Action == "core-cache" || op.Action == "remove-core" {
		command.WorkerID = op.Target
	}
	b, _ := json.Marshal(command)
	o, e := s.Store.Put(ctx, store.Object{Kind: "worker-commands", ID: id, Name: command.WorkerID + " " + op.Action, Data: b}, 0)
	if e != nil {
		return nil, e
	}
	s.notify()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, errCode("worker_operation_timeout")
		case <-ticker.C:
			result, e := s.Store.Get(ctx, "worker-results", id)
			if e != nil {
				continue
			}
			v, e := catalog.Decode[wire.Result](result.Data)
			if e != nil {
				return nil, e
			}
			s.Store.Delete(ctx, "worker-commands", id, o.Version)
			if !v.Success {
				return nil, errCode(v.Error)
			}
			return json.RawMessage(v.Data), nil
		}
	}
}
func (s *Server) workers(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method == "GET" {
		rows, e := s.Store.DB.Query(r.Context(), `SELECT id,seen_at,generation,status FROM worker_status ORDER BY id`)
		if e != nil {
			fail(w, 503, "workers_unavailable")
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id string
			var seen time.Time
			var generation int64
			var status json.RawMessage
			if rows.Scan(&id, &seen, &generation, &status) != nil {
				continue
			}
			out = append(out, map[string]any{"id": id, "seen_at": seen, "online": time.Since(seen) < 45*time.Second, "generation": generation, "status": status})
		}
		respond(w, 200, map[string]any{"items": out})
		return
	}
	if len(parts) == 3 && r.Method == "POST" && (parts[2] == "drain" || parts[2] == "resume" || parts[2] == "restart-core") {
		var payload json.RawMessage
		if !decode(w, r, &payload) {
			return
		}
		action := parts[2]
		if action == "resume" {
			action = "drain"
			payload = json.RawMessage(`{"enabled":false}`)
		}
		op, e := s.enqueue(r.Context(), action, parts[1], payload, r.Header.Get("Idempotency-Key"))
		if e != nil {
			fail(w, 503, "operation_enqueue_failed")
			return
		}
		respond(w, 202, s.redact(op))
		return
	}
	fail(w, 405, "method_not_allowed")
}

var _ = fmt.Sprintf
