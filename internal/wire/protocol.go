// Package wire defines the authenticated worker control protocol. JSON is explicit
// on gRPC to keep the schema inspectable without generated protobuf scaffolding.
package wire

import (
	"context"
	"encoding/json"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/topology"
)

type Codec struct{}

func (Codec) Name() string                    { return "relayweave-json" }
func (Codec) Marshal(v any) ([]byte, error)   { return json.Marshal(v) }
func (Codec) Unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func init()                                   { encoding.RegisterCodec(Codec{}) }

type Hello struct {
	Topology     *topology.Frame `json:"topology,omitempty"`
	WorkerID     string          `json:"worker_id"`
	Generation   int64           `json:"generation"`
	GuardVersion int64           `json:"guard_version,omitempty"`
	Status       json.RawMessage `json:"status,omitempty"`
	Error        string          `json:"error,omitempty"`
	CSR          []byte          `json:"csr,omitempty"`
	Results      []Result        `json:"results,omitempty"`
}
type Command struct {
	WorkerID string          `json:"worker_id,omitempty"`
	ID       string          `json:"id"`
	Action   string          `json:"action"`
	Target   string          `json:"target"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}
type Result struct {
	ID      string          `json:"id"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}
type Assignment struct {
	Generation  int64             `json:"generation"`
	Config      *config.Config    `json:"config,omitempty"`
	LiveGuard   *config.LiveGuard `json:"live_guard,omitempty"`
	Artifacts   []core.Artifact   `json:"artifacts,omitempty"`
	LeaseUntil  time.Time         `json:"lease_until"`
	Certificate []byte            `json:"certificate,omitempty"`
	Message     string            `json:"message,omitempty"`
	Commands    []Command         `json:"commands,omitempty"`
}

// SeenVersion is a receive cursor, never a durability acknowledgement.
// Hello.GuardVersion continues to acknowledge only durable checkpoints.
type AdmissionRequest struct {
	WorkerID    string `json:"worker_id"`
	SeenVersion int64  `json:"seen_version"`
}
type AdmissionResponse struct {
	LiveGuard *config.LiveGuard `json:"live_guard,omitempty"`
}

type Server interface {
	Sync(context.Context, *Hello) (*Assignment, error)
	Admission(context.Context, *AdmissionRequest) (*AdmissionResponse, error)
}

func Register(s *grpc.Server, handler Server) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "relayweave.Control",
		HandlerType: (*Server)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Sync",
			Handler: func(srv any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				in := new(Hello)
				if e := decode(in); e != nil {
					return nil, e
				}
				if interceptor == nil {
					return srv.(Server).Sync(ctx, in)
				}
				info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/relayweave.Control/Sync"}
				return interceptor(ctx, in, info, func(ctx context.Context, req any) (any, error) { return srv.(Server).Sync(ctx, req.(*Hello)) })
			},
		}, {
			MethodName: "Admission",
			Handler: func(srv any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				in := new(AdmissionRequest)
				if e := decode(in); e != nil {
					return nil, e
				}
				if interceptor == nil {
					return srv.(Server).Admission(ctx, in)
				}
				info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/relayweave.Control/Admission"}
				return interceptor(ctx, in, info, func(ctx context.Context, req any) (any, error) {
					return srv.(Server).Admission(ctx, req.(*AdmissionRequest))
				})
			},
		}},
	}, handler)
}
func Sync(ctx context.Context, conn *grpc.ClientConn, in *Hello) (*Assignment, error) {
	out := new(Assignment)
	e := conn.Invoke(ctx, "/relayweave.Control/Sync", in, out, grpc.ForceCodec(Codec{}))
	return out, e
}
func Admission(ctx context.Context, conn *grpc.ClientConn, in *AdmissionRequest) (*AdmissionResponse, error) {
	out := new(AdmissionResponse)
	e := conn.Invoke(ctx, "/relayweave.Control/Admission", in, out, grpc.ForceCodec(Codec{}))
	return out, e
}
