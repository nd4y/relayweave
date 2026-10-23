package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"proxyplatform/internal/wire"
)

func TestAdmissionRequiresTheEnrolledWorkerIdentityAndValidCursor(t *testing.T) {
	certificate := &x509.Certificate{Subject: pkix.Name{CommonName: "worker-one"}}
	verified := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}}}})
	for _, test := range []struct {
		name    string
		ctx     context.Context
		request wire.AdmissionRequest
		code    codes.Code
	}{
		{"no_peer", context.Background(), wire.AdmissionRequest{WorkerID: "worker-one"}, codes.Unauthenticated},
		{"wrong_worker", verified, wire.AdmissionRequest{WorkerID: "worker-two"}, codes.PermissionDenied},
		{"invalid_cursor", verified, wire.AdmissionRequest{WorkerID: "worker-one", SeenVersion: -1}, codes.InvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			// No database is attached: unauthenticated requests must be rejected
			// before any catalogue, status or journal access.
			_, err := (&Server{}).Admission(test.ctx, &test.request)
			if status.Code(err) != test.code {
				t.Fatalf("status=%v want=%v", status.Code(err), test.code)
			}
		})
	}
}
