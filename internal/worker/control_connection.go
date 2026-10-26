package worker

import (
	"crypto/tls"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
)

func newControlConnection(address string, tlsConfig *tls.Config, options ...grpc.DialOption) (*grpc.ClientConn, error) {
	// Docker removes stopped service names from DNS. Resolve ordinary host:port
	// endpoints during each bounded dial attempt, rather than inheriting the
	// separate gRPC DNS resolver's long negative-resolution backoff. Explicit
	// resolver schemes retain their operator-selected discovery behavior.
	if !strings.Contains(address, "://") {
		address = "passthrough:///" + address
	}
	base := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithConnectParams(grpc.ConnectParams{Backoff: backoff.Config{BaseDelay: time.Second, Multiplier: 1.6, Jitter: .2, MaxDelay: 5 * time.Second}, MinConnectTimeout: 3 * time.Second}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(32<<20), grpc.MaxCallSendMsgSize(8<<20)),
	}
	return grpc.NewClient(address, append(base, options...)...)
}
