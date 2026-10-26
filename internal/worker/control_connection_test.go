package worker

import (
	"context"
	"crypto/tls"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/test/bufconn"
	"proxyplatform/internal/wire"
)

// A DNS resolver with no usable address models a service removed from Docker
// DNS during an outage. Ordinary endpoints must reach the bounded dialer even
// while that resolver would still be backing off. No real DNS is queried.
type unavailableControlDNS struct{ builds atomic.Int64 }

func (b *unavailableControlDNS) Scheme() string { return "dns" }
func (b *unavailableControlDNS) Build(_ resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	b.builds.Add(1)
	cc.ReportError(&net.DNSError{Name: "offline-control", IsNotFound: true})
	return idleControlResolver{}, nil
}

type idleControlResolver struct{}

func (idleControlResolver) ResolveNow(resolver.ResolveNowOptions) {}
func (idleControlResolver) Close()                                {}

func TestControlConnectionRetriesNameAfterOfflineStart(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	fixture := &admissionFixture{}
	server := grpc.NewServer()
	wire.Register(server, fixture)
	go server.Serve(listener)
	defer server.Stop()
	dns := &unavailableControlDNS{}
	var available atomic.Bool
	attempted := make(chan struct{}, 1)
	var wrongAddress atomic.Bool
	conn, err := newControlConnection("offline-control:9443", &tls.Config{},
		grpc.WithResolvers(dns),
		// Only this in-process transport fixture overrides mTLS. Docker
		// acceptance separately exercises the unchanged production credentials.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
			if address != "offline-control:9443" {
				wrongAddress.Store(true)
			}
			if !available.Load() {
				select {
				case attempted <- struct{}{}:
				default:
				}
				return nil, &net.DNSError{Name: address, IsNotFound: true}
			}
			return listener.DialContext(ctx)
		}))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Connect()
	select {
	case <-attempted:
	case <-time.After(3 * time.Second):
		t.Fatal("offline service name never reached the bounded connection dialer")
	}
	available.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = wire.Admission(ctx, conn, &wire.AdmissionRequest{WorkerID: "worker-test"})
	for err != nil && ctx.Err() == nil {
		time.Sleep(20 * time.Millisecond)
		_, err = wire.Admission(ctx, conn, &wire.AdmissionRequest{WorkerID: "worker-test"})
	}
	if err != nil {
		t.Fatal("service did not reconnect after its name became available:", err)
	}
	if dns.builds.Load() != 0 || wrongAddress.Load() {
		t.Fatal("ordinary endpoint inherited resolver caching or lost its original hostname")
	}
}

func TestControlConnectionPreservesExplicitResolver(t *testing.T) {
	dns := &unavailableControlDNS{}
	conn, err := newControlConnection("dns:///offline-control:9443", &tls.Config{}, grpc.WithResolvers(dns))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := wire.Admission(ctx, conn, &wire.AdmissionRequest{}); err == nil || dns.builds.Load() != 1 {
		t.Fatal("explicit DNS resolver was not preserved")
	}
}
