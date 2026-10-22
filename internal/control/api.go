package control

import (
	"encoding/json"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"proxyplatform/internal/catalog"
	"proxyplatform/internal/config"
	"proxyplatform/internal/store"
)

var kinds = map[string]bool{"sources": true, "aggregates": true, "pools": true, "profiles": true, "policies": true, "listeners": true, "settings": true}

func pageBounds(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	return limit, max(0, min(offset, 1000000))
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/"), "/")
	kind := parts[0]
	if kind == "client-access" {
		s.clientAccessAPI(w, r, parts)
		return
	}
	if kind == "topology" {
		s.topologyAPI(w, r, parts)
		return
	}
	if kind == "overview" && r.Method == "GET" {
		s.overview(w, r)
		return
	}
	if kind == "events" {
		if len(parts) > 1 && parts[1] == "stream" {
			s.eventStream(w, r)
			return
		}
		v, e := s.Store.Events(r.Context(), 0)
		if e != nil {
			fail(w, 503, "event_store_unavailable")
			return
		}
		respond(w, 200, map[string]any{"items": v})
		return
	}
	if kind == "workers" {
		s.workers(w, r, parts)
		return
	}
	if kind == "cores" {
		s.cores(w, r, parts)
		return
	}
	if kind == "operations" {
		s.operations(w, r, parts)
		return
	}
	if kind == "config" {
		s.configuration(w, r, parts)
		return
	}
	if kind == "backup" {
		s.backup(w, r)
		return
	}
	if kind == "openapi.json" {
		s.openapi(w, r)
		return
	}
	if kind == "trash" {
		s.trash(w, r, parts)
		return
	}
	if kind == "support" {
		s.support(w, r)
		return
	}
	if !kinds[kind] {
		fail(w, 404, "not_found")
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case "GET":
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			if limit <= 0 || limit > 1000 {
				limit = 100
			}
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			if offset < 0 {
				offset = 0
			}
			objects, total, e := s.Store.List(r.Context(), kind, limit, offset)
			if e != nil {
				fail(w, 503, "object_store_unavailable")
				return
			}
			visible := []store.Object{}
			for _, o := range objects {
				visible = append(visible, s.redact(o))
			}
			respond(w, 200, map[string]any{"items": visible, "total": total, "limit": limit, "offset": offset})
			return
		case "POST":
			if kind == "profiles" || kind == "settings" {
				fail(w, 405, "use_import_or_patch")
				return
			}
			var body envelope
			if !decode(w, r, &body) {
				return
			}
			data, e := s.validate(kind, body.Data, nil)
			if e != nil {
				fail(w, 400, e.Error())
				return
			}
			name := strings.TrimSpace(body.Name)
			if name == "" || len(name) > 160 {
				fail(w, 400, "name_required")
				return
			}
			o, e := s.Store.Put(r.Context(), store.Object{Kind: kind, Name: name, Data: data}, 0)
			if e != nil {
				if errors.Is(e, store.ErrLimit) {
					fail(w, 409, "catalogue_budget_exceeded")
					return
				}
				fail(w, 503, "save_failed")
				return
			}
			s.Store.Event(r.Context(), kind, o.ID, "created", map[string]any{"name": o.Name})
			s.notify()
			w.Header().Set("ETag", strconv.FormatInt(o.Version, 10))
			respond(w, 201, s.redact(o))
			return
		default:
			fail(w, 405, "method_not_allowed")
			return
		}
	}
	id := parts[1]
	o, e := s.Store.Get(r.Context(), kind, id)
	if errors.Is(e, store.ErrNotFound) {
		fail(w, 404, "not_found")
		return
	}
	if e != nil {
		fail(w, 503, "object_store_unavailable")
		return
	}
	if len(parts) > 2 {
		action := parts[2]
		if kind == "sources" && action == "rollovers" && r.Method == "GET" {
			s.sourceRollovers(w, r, id)
			return
		}
		if r.Method == "GET" && action == "preview" {
			s.preview(w, r, o)
			return
		}
		if r.Method == "POST" && kind == "sources" && (action == "refresh" || action == "test" || action == "approve") {
			var payload json.RawMessage
			if action == "approve" {
				var body struct {
					Pending string `json:"pending_snapshot"`
					Version int64  `json:"source_version"`
				}
				if !decode(w, r, &body) {
					return
				}
				source, _ := catalog.Decode[catalog.Source](o.Data)
				if body.Version != o.Version || body.Pending == "" || body.Pending != source.PendingSnapshot {
					fail(w, 409, "review_snapshot_changed")
					return
				}
				payload, _ = json.Marshal(body)
			}
			op, e := s.enqueue(r.Context(), action, id, payload, r.Header.Get("Idempotency-Key"))
			if e != nil {
				fail(w, 503, "operation_enqueue_failed")
				return
			}
			respond(w, 202, s.redact(op))
			return
		}
		if r.Method == "POST" && kind == "profiles" && (action == "test" || action == "speed-test") {
			payload, e := s.profileCommand(r.Context(), o)
			if e != nil {
				fail(w, 400, safeError(e))
				return
			}
			name := "test-profile"
			if action == "speed-test" {
				name = "speed-test"
			}
			op, e := s.enqueue(r.Context(), name, id, payload, r.Header.Get("Idempotency-Key"))
			if e != nil {
				fail(w, 503, "operation_enqueue_failed")
				return
			}
			respond(w, 202, op)
			return
		}
		fail(w, 404, "unknown_action")
		return
	}
	switch r.Method {
	case "GET":
		w.Header().Set("ETag", strconv.FormatInt(o.Version, 10))
		respond(w, 200, s.redact(o))
	case "PATCH", "PUT":
		previous := o
		if version(r) != o.Version {
			fail(w, 409, "revision_conflict")
			return
		}
		var body envelope
		if !decode(w, r, &body) {
			return
		}
		data, e := s.validate(kind, body.Data, &o)
		if e != nil {
			fail(w, 400, e.Error())
			return
		}
		if body.Name != "" {
			if len(body.Name) > 160 {
				fail(w, 400, "name_too_long")
				return
			}
			o.Name = strings.TrimSpace(body.Name)
		}
		o.Data = data
		updated, e := s.Store.Put(r.Context(), o, o.Version)
		if errors.Is(e, store.ErrConflict) {
			fail(w, 409, "revision_conflict")
			return
		}
		if e != nil {
			fail(w, 503, "save_failed")
			return
		}
		s.Store.Event(r.Context(), kind, id, "updated", map[string]any{"version": updated.Version})
		s.notify()
		if kind == "profiles" || (kind == "sources" && sourceRuntimeChanged(previous, updated)) {
			s.queueReconcile(r.Context(), id)
		}
		w.Header().Set("ETag", strconv.FormatInt(updated.Version, 10))
		respond(w, 200, s.redact(updated))
	case "DELETE":
		if kind == "settings" || kind == "profiles" {
			fail(w, 405, "settings_cannot_be_deleted")
			return
		}
		if version(r) != o.Version {
			fail(w, 409, "revision_conflict")
			return
		}
		if e = s.softDelete(r, o); e != nil {
			fail(w, 409, "object_in_use_or_changed")
			return
		}
		if kind == "sources" {
			s.queueReconcile(r.Context(), id)
		}
		s.notify()
		respond(w, 200, map[string]bool{"deleted": true, "recoverable": true})
	default:
		fail(w, 405, "method_not_allowed")
	}
}

func (s *Server) redact(o store.Object) store.Object {
	if o.Kind == "profiles" {
		p, e := catalog.Decode[catalog.Profile](o.Data)
		if e != nil {
			o.Data = json.RawMessage(`{}`)
			return o
		}
		p.Outbound = nil
		o.Data, _ = json.Marshal(p)
		return o
	}
	var v map[string]any
	if json.Unmarshal(o.Data, &v) != nil {
		o.Data = json.RawMessage(`{}`)
		return o
	}
	if o.Kind == "sources" {
		if raw, ok := v["url"].(string); ok && raw != "" {
			if u, e := url.Parse(raw); e == nil {
				v["url_hint"] = u.Scheme + "://" + u.Host + "/••••"
			}
			v["url_configured"] = true
		}
		v["url"] = ""
		if headers, ok := v["headers"].(map[string]any); ok {
			for k := range headers {
				headers[k] = "••••"
			}
		}
	}
	if o.Kind == "listeners" {
		if v["password"] != "" && v["password"] != nil {
			v["password_configured"] = true
		}
		v["password"] = ""
	}
	if o.Kind == "operations" {
		delete(v, "payload")
	}
	if o.Kind == "policies" {
		for _, key := range []string{"probe_url", "speed_url"} {
			if raw, ok := v[key].(string); ok && raw != "" {
				v[key] = maskedURL(raw)
			}
		}
		if raw, ok := v["probe_fallback_urls"].([]any); ok {
			for i, x := range raw {
				if value, ok := x.(string); ok {
					raw[i] = maskedURL(value)
				}
			}
		}
	}
	o.Data, _ = json.Marshal(v)
	return o
}
func (s *Server) validate(kind string, patch json.RawMessage, old *store.Object) (json.RawMessage, error) {
	var in map[string]json.RawMessage
	if json.Unmarshal(patch, &in) != nil || in == nil {
		return nil, errCode("data_object_required")
	}
	all := map[string]json.RawMessage{}
	if old != nil {
		json.Unmarshal(old.Data, &all)
	} else {
		var defaults any
		switch kind {
		case "sources":
			defaults = catalog.Source{UpdatePolicy: "auto_safe", Enabled: true, IntervalSeconds: 3600, MaxStaleSeconds: 259200, CoreVersion: "26.10.17", Headers: map[string]string{}}
		case "aggregates":
			defaults = catalog.Aggregate{Dedupe: true}
		case "pools":
			defaults = catalog.Pool{Enabled: true, Automatic: len(in["source_ids"]) == 0 && len(in["profile_ids"]) == 0 && len(in["aggregate_id"]) == 0}
		case "policies":
			p := catalog.Policy{}
			p.Defaults()
			defaults = p
		case "listeners":
			protocol := "socks5"
			if string(in["protocol"]) == `"http"` {
				protocol = "http"
			}
			defaults = defaultAccessListener(protocol)

		}
		if defaults != nil {
			b, _ := json.Marshal(defaults)
			json.Unmarshal(b, &all)
		}
	}
	allowed := map[string]bool{}
	fields := map[string]string{
		"sources":    "url headers enabled interval_seconds max_stale_seconds allow_private core_version filter update_policy proxy_allow_cidrs",
		"aggregates": "source_ids prefix_names dedupe",
		"pools":      "aggregate_id source_ids profile_ids filter enabled automatic",
		"profiles":   "enabled core_version",
		"policies":   "mode pool_id active_paths warm_reserve sticky_seconds rotation_seconds probe_interval_seconds probe_url probe_scope probe_fallback_urls speed_url probe_budget_bytes max_connections connect_timeout_ms fail_closed disable_speed_probes",
		"listeners":  "protocol listen policy_id username password udp enabled allow_cidrs",
		"settings":   "default_core udp_advertise udp_port_min udp_port_max offline_lease_seconds download_mbps upload_mbps proxy_bind egress_paths",
	}
	for _, key := range strings.Fields(fields[kind]) {
		allowed[key] = true
	}
	for key, value := range in {
		if !allowed[key] {
			return nil, errCode("unknown_or_readonly_field")
		}
		if kind == "policies" && old != nil {
			if key == "probe_url" || key == "speed_url" {
				var raw, previous string
				json.Unmarshal(value, &raw)
				json.Unmarshal(all[key], &previous)
				if raw == maskedURL(previous) {
					continue
				}
			}
			if key == "probe_fallback_urls" {
				var values, previous []string
				json.Unmarshal(value, &values)
				json.Unmarshal(all[key], &previous)
				for i, v := range values {
					for _, p := range previous {
						if v == maskedURL(p) {
							values[i] = p
							break
						}
					}
				}
				value, _ = json.Marshal(values)
			}
		}
		if old != nil && (key == "url" || key == "password") && (string(value) == `""` || string(value) == `"••••"`) {
			continue
		}
		if key == "headers" && old != nil {
			var v, previous map[string]string
			json.Unmarshal(value, &v)
			json.Unmarshal(all[key], &previous)
			for k, val := range v {
				if val == "••••" {
					if saved, ok := previous[k]; ok {
						v[k] = saved
					} else {
						return nil, errCode("masked_header_without_original")
					}
				}
			}
			value, _ = json.Marshal(v)
		}
		all[key] = value
	}
	if kind == "listeners" {
		if raw, ok := in["password"]; ok && string(raw) != `""` && string(raw) != `"••••"` {
			var password string
			if json.Unmarshal(raw, &password) != nil || len(password) > 72 {
				return nil, errCode("proxy_password_max_72_bytes")
			}
			hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if e != nil {
				return nil, errCode("proxy_password_invalid")
			}
			all["password"], _ = json.Marshal(string(hash))
		}
		if string(in["username"]) == `""` && string(in["password"]) == `""` {
			all["password"] = json.RawMessage(`""`)
		}
	}
	if kind == "profiles" {
		if value, ok := in["core_version"]; ok {
			override := string(value) != `""`
			all["core_override"], _ = json.Marshal(override)
		}
	}
	b, _ := json.Marshal(all)
	if kind == "sources" && old != nil {
		before, _ := catalog.Decode[catalog.Source](old.Data)
		after, _ := catalog.Decode[catalog.Source](b)
		bh, _ := json.Marshal(before.Headers)
		ah, _ := json.Marshal(after.Headers)
		if before.URL != after.URL || string(bh) != string(ah) {
			after.ETag = ""
			after.LastModified = ""
			after.PendingSnapshot = ""
			after.NextRefresh = time.Time{}
			b, _ = json.Marshal(after)
		}
	}
	switch kind {
	case "sources":
		v, e := catalog.Decode[catalog.Source](b)
		if err := config.ValidateServerCIDRs(v.ProxyAllowCIDRs); err != nil {
			return nil, errCode("proxy_cidr_invalid")
		}
		if v.UpdatePolicy != "" && v.UpdatePolicy != "auto_safe" && v.UpdatePolicy != "review_only" {
			return nil, errCode("source_update_policy_invalid")
		}
		u, ue := url.Parse(v.URL)
		if e != nil || ue != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || len(v.Headers) > 32 || (v.IntervalSeconds != 0 && (v.IntervalSeconds < 60 || v.IntervalSeconds > 86400)) || v.MaxStaleSeconds < 60 || v.MaxStaleSeconds > 2592000 {
			return nil, errCode("source_settings_invalid")
		}
		if e = v.Filter.Validate(); e != nil {
			return nil, errCode("filter_invalid")
		}
		if v.CoreVersion != "" && !coreSelector.MatchString(v.CoreVersion) {
			return nil, errCode("invalid_core_version")
		}
	case "aggregates":
		v, e := catalog.Decode[catalog.Aggregate](b)
		if e != nil || len(v.SourceIDs) == 0 || len(v.SourceIDs) > 50 {
			return nil, errCode("aggregate_sources_required")
		}
		for _, id := range v.SourceIDs {
			if _, e = s.Store.Get(s.ctx, "sources", id); e != nil {
				return nil, errCode("source_reference_missing")
			}
		}
	case "pools":
		v, e := catalog.Decode[catalog.Pool](b)
		if e != nil || len(v.SourceIDs) > 50 || len(v.ProfileIDs) > 10000 {
			return nil, errCode("pool_invalid")
		}
		if v.AggregateID != "" {
			if _, e = s.Store.Get(s.ctx, "aggregates", v.AggregateID); e != nil {
				return nil, errCode("aggregate_reference_missing")
			}
		}
		if e = v.Filter.Validate(); e != nil {
			return nil, errCode("filter_invalid")
		}
	case "policies":
		v, e := catalog.Decode[catalog.Policy](b)
		if e != nil || v.Validate() != nil || !v.FailClosed {
			return nil, errCode("policy_invalid")
		}
		if _, e = s.Store.Get(s.ctx, "pools", v.PoolID); e != nil {
			return nil, errCode("pool_reference_missing")
		}
		if u, e := url.Parse(v.ProbeURL); e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, errCode("probe_url_invalid")
		}
		for _, raw := range append(v.ProbeFallbackURLs, v.SpeedURL) {
			if raw == "" {
				continue
			}
			u, e := url.Parse(raw)
			if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
				return nil, errCode("probe_url_invalid")
			}
		}
	case "listeners":
		v, e := catalog.Decode[catalog.Listener](b)
		host, p, e2 := net.SplitHostPort(v.Listen)
		port, _ := strconv.Atoi(p)
		if e != nil || e2 != nil || net.ParseIP(host) == nil || port < 1024 || port > 65535 || (v.Protocol != "http" && v.Protocol != "socks5") || (v.UDP && v.Protocol != "socks5") || (v.Username == "") != (v.Password == "") || len(v.Username) > 255 || len(v.Password) > 255 || len(v.AllowCIDRs) > 32 {
			return nil, errCode("listener_invalid")
		}
		for _, cidr := range v.AllowCIDRs {
			if _, _, e = net.ParseCIDR(cidr); e != nil {
				return nil, errCode("acl_cidr_invalid")
			}
		}
		if _, e = s.Store.Get(s.ctx, "policies", v.PolicyID); e != nil {
			return nil, errCode("policy_reference_missing")
		}
	case "profiles":
		v, e := catalog.Decode[catalog.Profile](b)
		if e != nil || v.Outbound == nil {
			return nil, errCode("profile_invalid")
		}
		if v.CoreVersion != "" && !coreSelector.MatchString(v.CoreVersion) {
			return nil, errCode("invalid_core_version")
		}
	case "settings":
		v, e := catalog.Decode[catalog.Settings](b)
		if config.ValidateEgressPaths(v.EgressPaths) != nil {
			return nil, errCode("egress_config_invalid")
		}
		if !coreSelector.MatchString(v.DefaultCore) {
			return nil, errCode("invalid_core_version")
		}
		if e != nil || (v.UDPAdvertise != "auto" && net.ParseIP(v.UDPAdvertise) == nil) || v.UDPPortMin < 1024 || v.UDPPortMax < v.UDPPortMin || v.UDPPortMax > 65535 || v.UDPPortMax-v.UDPPortMin > 1023 || v.OfflineLeaseSeconds < 60 || v.OfflineLeaseSeconds > 604800 || v.DownloadMbps < 1 || v.UploadMbps < 1 {
			return nil, errCode("settings_invalid")
		}
	}
	return b, nil
}
func maskedURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, e := url.Parse(raw)
	if e != nil {
		return "redacted"
	}
	return u.Scheme + "://" + u.Host + "/••••#" + tokenHash(raw)[:8]
}

type errCode string

func (e errCode) Error() string { return string(e) }
func (s *Server) softDelete(r *http.Request, o store.Object) error {
	// Tombstones preserve the encrypted object for undo; dependency checks happen before mutation.
	for kind, field := range map[string]string{"policies": "pool_id", "listeners": "policy_id"} {
		items, _, e := s.Store.List(r.Context(), kind, 10000, 0)
		if e != nil {
			return e
		}
		for _, v := range items {
			var data map[string]any
			json.Unmarshal(v.Data, &data)
			if data[field] == o.ID {
				return errCode("referenced_object")
			}
		}
	}
	for _, kind := range []string{"aggregates", "pools"} {
		items, _, e := s.Store.List(r.Context(), kind, 10000, 0)
		if e != nil {
			return e
		}
		for _, v := range items {
			var d map[string]any
			json.Unmarshal(v.Data, &d)
			for _, field := range []string{"aggregate_id", "source_ids", "profile_ids"} {
				if d[field] == o.ID {
					return errCode("referenced_object")
				}
				if ids, ok := d[field].([]any); ok {
					for _, id := range ids {
						if id == o.ID {
							return errCode("referenced_object")
						}
					}
				}
			}
		}
	}
	return s.atomicDelete(r.Context(), o)
}
