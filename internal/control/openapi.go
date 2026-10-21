package control

import (
	"encoding/json"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/core"
	"reflect"
	"strings"
	"time"
)

func schemaOf(t reflect.Type) map[string]any {
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return map[string]any{}
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer", "format": "int64"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": schemaOf(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaOf(t.Elem())}
	case reflect.Struct:
		properties := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if tag != "" && tag != "-" {
				properties[tag] = schemaOf(f.Type)
			}
		}
		return map[string]any{"type": "object", "properties": properties}
	default:
		return map[string]any{}
	}
}

// OpenAPI describes the actual HTTP contract. It contains no live configuration.
func OpenAPI() map[string]any {
	response := func(status, description string) map[string]any {
		return map[string]any{status: map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{}}}}}
	}
	operation := func(summary, status string) map[string]any {
		return map[string]any{"summary": summary, "responses": response(status, "Result or redacted object")}
	}
	csrf := map[string]any{"name": "X-CSRF-Token", "in": "header", "required": true, "schema": map[string]any{"type": "string"}}
	cas := map[string]any{"name": "If-Match", "in": "header", "required": true, "description": "Current numeric version, optionally enclosed in quotes", "schema": map[string]any{"type": "string"}}
	key := map[string]any{"name": "Idempotency-Key", "in": "header", "required": false, "schema": map[string]any{"type": "string", "maxLength": 128}}
	id := map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}
	schemas := map[string]any{}
	models := map[string]any{"Source": catalog.Source{}, "Aggregate": catalog.Aggregate{}, "Pool": catalog.Pool{}, "Profile": catalog.Profile{}, "Policy": catalog.Policy{}, "Listener": catalog.Listener{}, "Settings": catalog.Settings{}}
	for name, value := range models {
		schemas[name] = schemaOf(reflect.TypeOf(value))
	}
	for name, fields := range map[string]string{
		"Source":    "url headers enabled interval_seconds max_stale_seconds allow_private core_version filter",
		"Aggregate": "source_ids prefix_names dedupe", "Pool": "aggregate_id source_ids profile_ids filter enabled",
		"Profile": "enabled core_version", "Policy": "mode pool_id active_paths warm_reserve sticky_seconds rotation_seconds probe_interval_seconds probe_url probe_scope probe_fallback_urls speed_url probe_budget_bytes max_connections connect_timeout_ms fail_closed",
		"Listener": "protocol listen policy_id username password udp enabled allow_cidrs",
		"Settings": "default_core udp_advertise udp_port_min udp_port_max offline_lease_seconds download_mbps upload_mbps proxy_bind",
	} {
		properties := schemas[name].(map[string]any)["properties"].(map[string]any)
		writable := map[string]any{}
		for _, field := range strings.Fields(fields) {
			writable[field] = properties[field]
		}
		schemas[name+"Input"] = map[string]any{"type": "object", "properties": writable, "additionalProperties": false}
	}
	delete(schemas["Profile"].(map[string]any)["properties"].(map[string]any), "outbound")
	schemas["ArtifactApproval"] = schemaOf(reflect.TypeOf(core.Artifact{}))
	schemas["CoreCacheRequest"] = map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"worker_id": map[string]any{"type": "string", "default": "worker-1"}, "version": map[string]any{"type": "string", "description": "Required for removal; artifact selector, including optional variant"}}}
	paths := map[string]any{}
	add := func(path, method, summary, status string, body map[string]any, async bool) {
		entry, ok := paths[path].(map[string]any)
		if !ok {
			entry = map[string]any{}
			paths[path] = entry
		}
		op := operation(summary, status)
		if method != "get" {
			parameters := []any{csrf}
			if async {
				parameters = append(parameters, key)
			}
			op["parameters"] = parameters
		}
		if strings.Contains(path, "{id}") {
			entry["parameters"] = []any{id}
		}
		if body != nil {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": body}}}
		}
		entry[method] = op
	}
	for kind, model := range map[string]string{"sources": "Source", "aggregates": "Aggregate", "pools": "Pool", "profiles": "Profile", "policies": "Policy", "listeners": "Listener", "settings": "Settings"} {
		path := "/api/v1/" + kind
		add(path, "get", "List "+kind+" with redacted secrets", "200", nil, false)
		entry := paths[path].(map[string]any)
		entry["parameters"] = []any{map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "default": 100}}, map[string]any{"name": "offset", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 0, "default": 0}}}
		envelope := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "maxLength": 160}, "data": map[string]any{"$ref": "#/components/schemas/" + model + "Input"}}, "required": []string{"data"}}
		if kind != "profiles" && kind != "settings" {
			add(path, "post", "Create "+kind, "201", envelope, false)
		}
		add(path+"/{id}", "get", "Read "+model, "200", nil, false)
		add(path+"/{id}", "patch", "Patch writable fields with optimistic concurrency", "200", envelope, false)
		paths[path+"/{id}"].(map[string]any)["patch"].(map[string]any)["parameters"] = []any{csrf, cas}
		if kind != "settings" && kind != "profiles" {
			add(path+"/{id}", "delete", "Move to encrypted trash for 24 hours", "200", nil, false)
			paths[path+"/{id}"].(map[string]any)["delete"].(map[string]any)["parameters"] = []any{csrf, cas}
		}
	}
	for _, path := range []string{"overview", "events", "events/stream", "workers", "cores", "operations", "config", "trash", "support"} {
		add("/api/v1/"+path, "get", "Read "+path, "200", nil, false)
	}
	for _, path := range []string{"sources/{id}/preview", "pools/{id}/preview", "operations/{id}"} {
		add("/api/v1/"+path, "get", "Read "+path, "200", nil, false)
	}
	for _, path := range []string{"sources/{id}/refresh", "sources/{id}/test", "sources/{id}/approve", "profiles/{id}/test", "profiles/{id}/speed-test", "config/apply", "config/rollback", "cores/install", "cores/approve", "cores/cache", "cores/remove", "workers/{id}/drain", "workers/{id}/resume", "workers/{id}/restart-core"} {
		add("/api/v1/"+path, "post", path+" (poll /operations/ID for completion)", "202", map[string]any{"type": "object"}, true)
	}
	for path, model := range map[string]string{"cores/approve": "ArtifactApproval", "cores/cache": "CoreCacheRequest", "cores/remove": "CoreCacheRequest"} {
		paths["/api/v1/"+path].(map[string]any)["post"].(map[string]any)["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/" + model}}}}
	}
	paths["/api/v1/pools/{id}/preview"].(map[string]any)["get"].(map[string]any)["parameters"] = []any{map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "default": 100}}, map[string]any{"name": "offset", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 0, "default": 0}}}
	for _, path := range []string{"config/plan", "operations/{id}/cancel", "trash/{id}/restore", "backup", "support", "auth/logout"} {
		add("/api/v1/"+path, "post", path, "200", map[string]any{"type": "object"}, false)
	}
	for _, path := range []string{"backup/preview", "backup/restore"} {
		add("/api/v1/"+path, "post", path, "200", nil, false)
		paths["/api/v1/"+path].(map[string]any)["post"].(map[string]any)["requestBody"] = map[string]any{"required": true, "content": map[string]any{"multipart/form-data": map[string]any{"schema": map[string]any{"type": "object", "required": []string{"file"}, "properties": map[string]any{"file": map[string]any{"type": "string", "format": "binary"}, "password": map[string]any{"type": "string", "format": "password"}}}}}}
	}
	add("/api/v1/auth/login", "post", "Password-only administrator login", "200", map[string]any{"type": "object", "required": []string{"password"}, "properties": map[string]any{"password": map[string]any{"type": "string", "format": "password", "maxLength": 256}}}, false)
	login := paths["/api/v1/auth/login"].(map[string]any)["post"].(map[string]any)
	login["security"] = []any{}
	delete(login, "parameters")
	add("/api/v1/auth/session", "get", "Read session and CSRF token", "200", nil, false)
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "RelayWeave API", "version": "0.1.0", "description": "Single administrator. All mutations require a session and CSRF token. Errors use {error:{code}}. CAS conflicts return 409. Long operations return 202 and preserve a durable status. Secrets in GET are blank or masked; resubmit the returned mask to preserve a policy URL."}, "servers": []any{map[string]any{"url": "/"}}, "security": []any{map[string]any{"session": []string{}}}, "components": map[string]any{"schemas": schemas, "securitySchemes": map[string]any{"session": map[string]any{"type": "apiKey", "in": "cookie", "name": "rw_session"}}}, "paths": paths}
}
