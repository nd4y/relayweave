package core

import (
	"net"
	"testing"

	"proxyplatform/internal/config"
)

func TestPrivateListenerMustExistBeforeUpstreamHealthRuns(t *testing.T) {
	if localListenersReady([]config.Route{{Endpoint: "127.0.0.1:0"}}) {
		t.Fatal("a missing private listener was treated as ready")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	routes := []config.Route{{Endpoint: listener.Addr().String()}}
	if !localListenersReady(routes) {
		t.Fatal("a real private listener was not detected")
	}
	listener.Close()
	if localListenersReady(routes) {
		t.Fatal("a closed listener was treated as an upstream health result")
	}
}
