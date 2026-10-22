package control

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
	"proxyplatform/internal/wire"
)

func authenticatedWorker(ctx context.Context, id string) error {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "worker identity required")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 || tlsInfo.State.PeerCertificates[0].Subject.CommonName != id || !strings.HasPrefix(id, "worker-") || len(id) > 48 {
		return status.Error(codes.PermissionDenied, "worker not enrolled")
	}
	return nil
}

// A read-only admission poll bypasses the durable status/command writes in Sync.
func (s *Server) Admission(ctx context.Context, request *wire.AdmissionRequest) (*wire.AdmissionResponse, error) {
	if err := authenticatedWorker(ctx, request.WorkerID); err != nil {
		return nil, err
	}
	if request.SeenVersion < 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid admission cursor")
	}
	guard, err := s.liveGuard(ctx, request.SeenVersion)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "admission fence unavailable")
	}
	return &wire.AdmissionResponse{LiveGuard: guard}, nil
}

func (s *Server) Sync(ctx context.Context, hello *wire.Hello) (*wire.Assignment, error) {
	if err := authenticatedWorker(ctx, hello.WorkerID); err != nil {
		return nil, err
	}
	if len(hello.Status) > 4<<20 || len(hello.Results) > 100 {
		return nil, status.Error(codes.ResourceExhausted, "message limit")
	}
	workerStatus := hello.Status
	if hello.Topology != nil && s.Topology != nil {
		s.Topology.Accept(hello.WorkerID, *hello.Topology)
	}
	if !json.Valid(workerStatus) {
		workerStatus = json.RawMessage(`{}`)
	}
	if _, e := s.Store.DB.Exec(ctx, `INSERT INTO worker_status(id,generation,status) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET seen_at=now(),generation=excluded.generation,status=excluded.status`, hello.WorkerID, hello.Generation, workerStatus); e != nil {
		return nil, status.Error(codes.Unavailable, "database unavailable")
	}
	for _, result := range hello.Results {
		if len(result.ID) > 160 || len(result.Data) > 1<<20 {
			continue
		}
		queued, e := s.Store.Get(ctx, "worker-commands", result.ID)
		if e != nil {
			continue
		}
		command, e := catalog.Decode[wire.Command](queued.Data)
		if e != nil || !commandForWorker(command, hello.WorkerID) {
			continue
		}
		if _, e := s.Store.Get(ctx, "worker-results", result.ID); e == nil {
			continue
		}
		b, _ := json.Marshal(result)
		s.Store.Put(ctx, store.Object{Kind: "worker-results", ID: result.ID, Name: result.ID, Data: b}, 0)
	}
	settings, e := s.settings(ctx)
	if e != nil {
		return nil, status.Error(codes.Unavailable, "settings unavailable")
	}
	assignment := &wire.Assignment{LeaseUntil: time.Now().Add(time.Duration(settings.OfflineLeaseSeconds) * time.Second)}
	if s.Compute != nil {
		assignment.ComputeEpoch = s.Compute.Epoch.Load()
		// Compute is optional; errors must not prevent security/config Sync.
		if len(hello.ComputeSnapshots) <= 2 {
			for _, snapshot := range hello.ComputeSnapshots {
				if snapshot.Executor == hello.WorkerID && snapshot.Generation == hello.Generation {
					_ = s.Compute.Submit(snapshot)
				}
			}
		}
		assignment.ComputeRecommendations = s.Compute.CachedLatest(hello.WorkerID)
	}
	assignment.LiveGuard, e = s.liveGuard(ctx, hello.GuardVersion)
	if e != nil {
		return nil, status.Error(codes.Unavailable, "admission fence unavailable")
	}
	if len(hello.CSR) > 0 && len(hello.CSR) < 8192 {
		bundle, e := s.Authority.Issue(hello.WorkerID, false, hello.CSR)
		if e != nil {
			return nil, status.Error(codes.InvalidArgument, "CSR invalid")
		}
		assignment.Certificate = bundle.Certificate
	}
	commands, _, e := s.Store.List(ctx, "worker-commands", 100, 0)
	if e == nil {
		for _, o := range commands {
			if _, e := s.Store.Get(ctx, "worker-results", o.ID); e == nil {
				continue
			}
			command, e := catalog.Decode[wire.Command](o.Data)
			if e == nil && commandForWorker(command, hello.WorkerID) {
				assignment.Commands = append(assignment.Commands, command)
			}
		}
	}
	desired, e := s.Store.Get(ctx, "internal", "desired")
	if e != nil {
		assignment.Message = "awaiting_configuration"
		return assignment, nil
	}
	var pointer struct {
		RevisionID string `json:"revision_id"`
		Generation int64  `json:"generation"`
	}
	if json.Unmarshal(desired.Data, &pointer) != nil {
		return nil, status.Error(codes.Internal, "desired revision corrupt")
	}
	assignment.Generation = pointer.Generation
	if pointer.Generation > hello.Generation {
		o, e := s.Store.Get(ctx, "revisions", pointer.RevisionID)
		if e != nil {
			return nil, status.Error(codes.Unavailable, "revision unavailable")
		}
		revision, e := catalog.Decode[Revision](o.Data)
		if e != nil {
			return nil, status.Error(codes.Internal, "revision corrupt")
		}
		revision.Config.LeaseUntil = assignment.LeaseUntil
		assignment.Config = &revision.Config
		artifacts, e := s.approvedArtifacts(ctx)
		if e != nil {
			return nil, status.Error(codes.Unavailable, "artifact catalog unavailable")
		}
		assignment.Artifacts = artifacts
	}
	return assignment, nil
}

func commandForWorker(c wire.Command, id string) bool {
	target := c.WorkerID
	if target == "" {
		target = "worker-1"
	}
	return target == id
}
