package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"proxyplatform/internal/config"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Subscription is an isolated fixture, deliberately excluded from production commands.
func Subscription(ctx context.Context, dir string) error {
	c, e := config.Load(dir + "/worker.json")
	if e != nil {
		return e
	}
	ca, e := os.ReadFile(dir + "/ca.pem")
	if e != nil {
		return e
	}
	var outbound map[string]any
	json.Unmarshal(c.Routes[0].Outbound, &outbound)
	tls := outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
	tls["certificates"] = []any{map[string]any{"certificate": strings.Split(strings.TrimSpace(string(ca)), "\n"), "usage": "verify"}}
	var revision atomic.Int64
	revision.Store(1)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /revision", func(w http.ResponseWriter, r *http.Request) { revision.Add(1); w.Write([]byte("ok")) })
	mux.HandleFunc("/subscription", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		etag := fmt.Sprintf(`"lab-%d"`, revision.Load())
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(304)
			return
		}
		primary := map[string]any{}
		b, _ := json.Marshal(outbound)
		json.Unmarshal(b, &primary)
		if host := r.URL.Query().Get("upstream"); host == "shaper-a" || host == "shaper-b" {
			primary["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)["address"] = host
		}
		primary["tag"] = fmt.Sprintf("DE Lab %d", revision.Load())
		if r.URL.Query().Get("network") == "ws" {
			stream := primary["streamSettings"].(map[string]any)
			stream["network"] = "ws"
			stream["wsSettings"] = map[string]any{"path": "/fixture"}
		}
		rows := []any{primary}
		if r.URL.Query().Get("variant") == "ambiguous" {
			data, _ := json.Marshal(primary)
			var second map[string]any
			json.Unmarshal(data, &second)
			second["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)["id"] = "00000000-0000-0000-0000-000000000002"
			second["tag"] = "DE ambiguous fixture"
			rows = append(rows, second)
		}
		json.NewEncoder(w).Encode(map[string]any{"outbounds": rows})
	})
	mux.HandleFunc("/scale", func(w http.ResponseWriter, r *http.Request) {
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		if count < 1 || count > 2000 || start < 1 || start+count > 32000 {
			http.Error(w, "fixture limits", 400)
			return
		}
		etag := fmt.Sprintf(`"scale-%d-%d"`, start, count)
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(304)
			return
		}
		base, _ := json.Marshal(outbound)
		rows := make([]any, 0, count)
		for i := range count {
			var p map[string]any
			json.Unmarshal(base, &p)
			p["tag"] = fmt.Sprintf("DE scale %05d", start+i)
			p["streamSettings"].(map[string]any)["sockopt"] = map[string]any{"tcpKeepAliveInterval": start + i}
			rows = append(rows, p)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"outbounds": rows})
	})
	mux.HandleFunc("/html", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<!doctype html><html>Login</html>")) })
	mux.HandleFunc("/empty", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("")) })
	mux.HandleFunc("/unauthorized", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	mux.HandleFunc("/timeout", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	})
	server := &http.Server{Addr: ":9004", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		server.Close()
		return nil
	case e := <-done:
		return e
	}
}
