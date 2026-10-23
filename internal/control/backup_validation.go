package control

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/core"
	"proxyplatform/internal/store"
)

func validateBackup(in store.Export) error {
	if in.Schema != 1 || len(in.Objects) > 50000 {
		return fmt.Errorf("backup schema or limit")
	}
	objects := map[string]bool{}
	counts := map[string]int{}
	for _, o := range in.Objects {
		counts[o.Kind]++
		if budget := store.KindBudget(o.Kind); budget > 0 && counts[o.Kind] > budget {
			return fmt.Errorf("object budget exceeded")
		}
		if o.ID == "" || o.Name == "" || len(o.Data) > 8<<20 || !json.Valid(o.Data) || objects[o.Kind+"/"+o.ID] {
			return fmt.Errorf("invalid object")
		}
		objects[o.Kind+"/"+o.ID] = true
	}
	require := func(kind, id string) bool { return objects[kind+"/"+id] }
	for _, o := range in.Objects {
		switch o.Kind {
		case "sources":
			v, e := catalog.Decode[catalog.Source](o.Data)
			u, err := url.Parse(v.URL)
			if e != nil || err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || v.Filter.Validate() != nil || v.MaxStaleSeconds < 60 {
				return fmt.Errorf("source invalid")
			}
		case "aggregates":
			v, e := catalog.Decode[catalog.Aggregate](o.Data)
			if e != nil {
				return e
			}
			for _, id := range v.SourceIDs {
				if !require("sources", id) {
					return fmt.Errorf("source missing")
				}
			}
		case "pools":
			v, e := catalog.Decode[catalog.Pool](o.Data)
			if e != nil || v.Filter.Validate() != nil {
				return fmt.Errorf("pool invalid")
			}
			if v.AggregateID != "" && !require("aggregates", v.AggregateID) {
				return fmt.Errorf("aggregate missing")
			}
			for _, id := range v.SourceIDs {
				if !require("sources", id) {
					return fmt.Errorf("source missing")
				}
			}
			for _, id := range v.ProfileIDs {
				if !require("profiles", id) {
					return fmt.Errorf("profile missing")
				}
			}
		case "policies":
			v, e := catalog.Decode[catalog.Policy](o.Data)
			if e != nil || v.Validate() != nil || !require("pools", v.PoolID) {
				return fmt.Errorf("policy invalid")
			}
		case "profiles":
			v, e := catalog.Decode[catalog.Profile](o.Data)
			if e != nil || !require("sources", v.SourceID) {
				return fmt.Errorf("profile invalid")
			}
			if v.Supported {
				b, _ := json.Marshal(map[string]any{"outbounds": []json.RawMessage{v.Outbound}})
				parsed, e := catalog.Parse(b)
				if e != nil || len(parsed.Profiles) != 1 || !parsed.Profiles[0].Supported {
					return fmt.Errorf("profile outbound invalid")
				}
			}
		case "listeners":
			v, e := catalog.Decode[catalog.Listener](o.Data)
			if e != nil || !require("policies", v.PolicyID) || (v.Protocol != "http" && v.Protocol != "socks5") {
				return fmt.Errorf("listener invalid")
			}
			if _, _, e = net.SplitHostPort(v.Listen); e != nil {
				return e
			}
		case "settings":
			v, e := catalog.Decode[catalog.Settings](o.Data)
			if o.ID != "global" || e != nil || (v.UDPAdvertise != "auto" && net.ParseIP(v.UDPAdvertise) == nil) || v.OfflineLeaseSeconds < 60 || v.UDPPortMin < 1024 || v.UDPPortMax < v.UDPPortMin || v.UDPPortMax > 65535 {
				return fmt.Errorf("settings invalid")
			}
		case "core-artifacts":
			v, e := catalog.Decode[core.Artifact](o.Data)
			if e != nil || !coreVersion.MatchString(v.Version) || len(v.SHA256) != 64 || !v.ValidSource() {
				return fmt.Errorf("artifact invalid")
			}
			if v.Variant != "" && validateCustomArtifact(v) != nil {
				return fmt.Errorf("custom artifact not allowed")
			}
		default:
			return fmt.Errorf("unknown object kind")
		}
	}
	if !require("settings", "global") {
		return fmt.Errorf("settings missing")
	}
	return nil
}
