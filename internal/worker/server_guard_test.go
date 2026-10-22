package worker

import (
	"context"
	"encoding/json"
	"proxyplatform/internal/config"
	"testing"
)

func TestServerGuardRejectsMetadataEvenWithPrivateException(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "169.254.169.254", "100.100.100.200", "::ffff:127.0.0.1"} {
		_, _, err := checkedServer(context.Background(), config.Route{CheckServer: true, AllowPrivate: true, ServerHost: host})
		if err == nil {
			t.Fatal("forbidden actual dial", host)
		}
	}
}

func TestServerPinDoesNotMutateImmutableRoute(t *testing.T) {
	route := config.Route{CheckServer: true, ServerHost: "8.8.8.8", Outbound: json.RawMessage(`{"protocol":"vless","settings":{"vnext":[{"address":"8.8.8.8","port":443}]},"streamSettings":{"security":"tls","tlsSettings":{"serverName":"peer.example"}}}`)}
	before := string(route.Outbound)
	pinned, ip, err := checkedServer(context.Background(), route)
	if err != nil || ip != "8.8.8.8" || string(route.Outbound) != before || !json.Valid(pinned.Outbound) {
		t.Fatal("pin or immutable config", err)
	}
}

func TestSourcePrivateAccessDoesNotAuthorizePrivateProxy(t *testing.T) {
	route := config.Route{CheckServer: true, AllowPrivate: true, ServerHost: "172.18.0.3", Outbound: json.RawMessage(`{"protocol":"socks","settings":{"servers":[{"address":"172.18.0.3","port":1080}]}}`)}
	if _, _, err := checkedServer(context.Background(), route); err == nil {
		t.Fatal("source exception widened to proxy access")
	}
	route.ServerAllowCIDRs = []string{"172.18.0.3/32"}
	if _, ip, err := checkedServer(context.Background(), route); err != nil || ip != "172.18.0.3" {
		t.Fatal("explicit target exception unavailable", err)
	}
}
