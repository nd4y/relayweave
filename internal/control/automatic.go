package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
	"strconv"
)

// Bootstrap only a new installation. Never replace an operator's listeners or
// publish an unauthenticated LAN listener as a side effect of adding a source.
func (s *Server) ensureAutomaticDefaults(ctx context.Context) (bool, error) {
	release, err := s.Store.AdvisoryLock(ctx, 723502001)
	if err != nil {
		return false, err
	}
	defer release()
	if _, err = s.Store.Get(ctx, "internal", "desired"); err == nil {
		return false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	items, _, err := s.Store.List(ctx, "listeners", 1, 0)
	if err != nil {
		return false, err
	}
	if len(items) > 0 {
		all, _, e := s.Store.List(ctx, "listeners", 32, 0)
		if e != nil {
			return false, e
		}
		for _, o := range all {
			if o.ID != "automatic-http" && o.ID != "automatic-socks5" {
				return false, nil
			}
		}
	}
	put := func(kind, id string, v any) error {
		if _, e := s.Store.Get(ctx, kind, id); e == nil {
			return nil
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		b, _ := json.Marshal(v)
		_, e := s.Store.Put(ctx, store.Object{Kind: kind, ID: id, Name: "Automatic", Data: b}, 0)
		return e
	}
	if err = put("pools", "automatic", catalog.Pool{Enabled: true, Automatic: true}); err != nil {
		return false, err
	}
	p := catalog.Policy{PoolID: "automatic"}
	p.Defaults()
	if err = put("policies", "automatic", p); err != nil {
		return false, err
	}
	for _, protocol := range []string{"http", "socks5"} {
		l := defaultAccessListener(protocol)
		l.Listen = net.JoinHostPort("127.0.0.1", strconv.Itoa(accessDefaultPort(protocol)))
		l.PolicyID = "automatic"
		if err = put("listeners", "automatic-"+protocol, l); err != nil {
			return false, err
		}
	}
	return true, nil
}
