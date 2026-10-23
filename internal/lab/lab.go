// Package lab contains synthetic fixtures for the prototype, never provider credentials.
package lab

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"proxyplatform/internal/config"
)

const LabUUID = "00000000-0000-4000-8000-000000000001"

func jsonFile(path string, value any) error {
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, b, 0644)
}
func Init(dir string) error {
	// Keep the lab identity/configuration stable across Compose restart/recreate.
	if _, err := os.Stat(filepath.Join(dir, "worker.json")); err == nil {
		return nil
	}
	if e := os.MkdirAll(filepath.Join(dir, "state"), 0700); e != nil {
		return e
	}
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return e
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "proxy-platform synthetic lab"}, DNSNames: []string{"lab-upstream", "origin"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(7 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	cert, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		return e
	}
	der, e := x509.MarshalECPrivateKey(key)
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(dir, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), 0644); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0600); e != nil {
		return e
	}
	upstream := map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": []any{map[string]any{"listen": "0.0.0.0", "port": 2443, "protocol": "vless", "settings": map[string]any{"clients": []any{map[string]any{"id": LabUUID}}, "decryption": "none"}, "streamSettings": map[string]any{"network": "tcp", "security": "tls", "tlsSettings": map[string]any{"certificates": []any{map[string]any{"certificateFile": dir + "/ca.pem", "keyFile": dir + "/key.pem"}}}}}}, "outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{}}}}
	if e = jsonFile(filepath.Join(dir, "upstream.json"), upstream); e != nil {
		return e
	}
	outbound := map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "upstream", "port": 2443, "users": []any{map[string]any{"id": LabUUID, "encryption": "none"}}}}}, "streamSettings": map[string]any{"network": "tcp", "security": "tls", "tlsSettings": map[string]any{"serverName": "lab-upstream", "allowInsecure": false, "disableSystemRoot": true, "certificates": []any{map[string]any{"certificateFile": dir + "/ca.pem", "usage": "verify"}}}}, "mux": map[string]any{"enabled": false}}
	ob, _ := json.Marshal(outbound)
	c := config.Config{Cores: []config.Core{{ID: "primary", Version: "26.10.17", Binary: "/opt/xray/builtin/xray"}, {ID: "secondary", Version: "26.9.30", Binary: "/cores/v26.9.30/xray"}}, Routes: []config.Route{{ID: "primary", Core: "primary", Endpoint: "127.0.0.1:19001", Outbound: ob}, {ID: "secondary", Core: "secondary", Endpoint: "127.0.0.1:19002", Outbound: ob}}, StateDir: dir + "/state", MetricsListen: "0.0.0.0:9090", ProbeURL: "http://origin:9000/healthz", ConnectTimeoutMS: 3000, MaxConnections: 1024, UDP: config.UDP{Bind: "0.0.0.0", Advertise: "127.0.0.1", PortMin: 30000, PortMax: 30063, IdleSeconds: 60}}
	c.Listeners = []config.Listener{{ID: "http", Protocol: "http", Listen: "0.0.0.0:8080"}, {ID: "http-auth", Protocol: "http", Listen: "0.0.0.0:8081", Username: "lab-user", Password: "lab-pass"}, {ID: "http-primary", Protocol: "http", Listen: "0.0.0.0:8082", Route: "primary"}, {ID: "http-secondary", Protocol: "http", Listen: "0.0.0.0:8083", Route: "secondary"}, {ID: "socks", Protocol: "socks5", Listen: "0.0.0.0:1080", UDP: true}, {ID: "socks-auth", Protocol: "socks5", Listen: "0.0.0.0:1081", Username: "lab-user", Password: "lab-pass", UDP: true}, {ID: "socks-primary", Protocol: "socks5", Listen: "0.0.0.0:1082", Route: "primary", UDP: true}, {ID: "socks-secondary", Protocol: "socks5", Listen: "0.0.0.0:1083", Route: "secondary", UDP: true}}
	if e = c.Validate(); e != nil {
		return e
	}
	if e = jsonFile(filepath.Join(dir, "worker.json"), c); e != nil {
		return e
	}
	for _, id := range []string{"primary", "secondary"} {
		base := map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": []any{map[string]any{"listen": "0.0.0.0", "port": 1080, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true, "ip": "127.0.0.1"}}, map[string]any{"listen": "0.0.0.0", "port": 8080, "protocol": "http", "settings": map[string]any{}}}, "outbounds": []any{outbound}}
		if e = jsonFile(filepath.Join(dir, "baseline-"+id+".json"), base); e != nil {
			return e
		}
	}
	if os.Getuid() == 0 {
		return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return os.Chown(path, 65532, 65532)
		})
	}
	return nil
}

// PrepareUpstream derives exact fixture IPs after origin has started. No broad private-network allow.
func PrepareUpstream(source, destination string) error {
	ips, err := net.LookupIP("origin")
	if err != nil {
		return err
	}
	allowed := []string{}
	for _, ip := range ips {
		allowed = append(allowed, ip.String())
	}
	if len(allowed) == 0 {
		return fmt.Errorf("origin has no addresses")
	}
	b, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	var c map[string]any
	if err = json.Unmarshal(b, &c); err != nil {
		return err
	}
	c["log"] = map[string]any{"loglevel": "warning", "access": "none"}
	settings := c["outbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
	settings["finalRules"] = []any{
		map[string]any{"action": "allow", "network": "tcp", "ip": allowed, "port": "9000,9001,9443"},
		map[string]any{"action": "allow", "network": "udp", "ip": allowed, "port": "9002,9003,9004,9005,9444"},
	}
	return jsonFile(destination, c)
}

func Origin(ctx context.Context, dir string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Lab-Origin", "isolated")
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/bytes", func(w http.ResponseWriter, r *http.Request) {
		n, e := strconv.ParseInt(r.URL.Query().Get("n"), 10, 64)
		if e != nil || n < 0 || n > 1<<30 {
			http.Error(w, "invalid n", 400)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		w.Header().Set("X-Lab-Origin", "isolated")
		buf := make([]byte, 128<<10)
		for i := range buf {
			buf[i] = 'x'
		}
		for n > 0 {
			size := min(n, int64(len(buf)))
			written, e := w.Write(buf[:size])
			if e != nil {
				return
			}
			n -= int64(written)
		}
	})
	// Deterministic transport-only fault on an already allowed lab port. No
	// application response bytes are sent; topology must not invent a status.
	mux.HandleFunc("/transport-close", func(w http.ResponseWriter, r *http.Request) {
		peer, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			peer.Close()
		}
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(r.Body, 32<<20))
		if e != nil {
			http.Error(w, "read", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"bytes": n, "sha256": hex.EncodeToString(h.Sum(nil)), "proxy_authorization": r.Header.Get("Proxy-Authorization"), "hop_header": r.Header.Get("X-Hop")})
	})
	mux.HandleFunc("/chunked", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "X-Lab-Trailer")
		w.WriteHeader(200)
		io.WriteString(w, "chunk one\n")
		w.(http.Flusher).Flush()
		io.WriteString(w, "chunk two\n")
		w.Header().Set("X-Lab-Trailer", "complete")
	})
	server := &http.Server{Addr: ":9000", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	tlsServer := &http.Server{Addr: ":9443", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errch := make(chan error, 8)
	go func() { errch <- server.ListenAndServe() }()
	go func() { errch <- tlsServer.ListenAndServeTLS(dir+"/ca.pem", dir+"/key.pem") }()
	go func() { errch <- QUICOrigin(ctx, dir) }()
	tcp, e := net.Listen("tcp", ":9001")
	if e != nil {
		return e
	}
	defer tcp.Close()
	go func() {
		for {
			c, e := tcp.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	var udps []*net.UDPConn
	for _, p := range []int{9002, 9003, 9004, 9005} {
		s, e := net.ListenUDP("udp", &net.UDPAddr{Port: p})
		if e != nil {
			return e
		}
		udps = append(udps, s)
		go func(p int, s *net.UDPConn) {
			buf := make([]byte, 65535)
			for {
				n, a, e := s.ReadFromUDP(buf)
				if e != nil {
					return
				}
				out := buf[:n]
				if p == 9004 {
					continue
				} // A one-way upload must keep the relay alive.
				if p == 9005 {
					go func(target *net.UDPAddr) {
						tick := time.NewTicker(time.Second)
						defer tick.Stop()
						until := time.NewTimer(90 * time.Second)
						defer until.Stop()
						for {
							select {
							case <-ctx.Done():
								return
							case <-until.C:
								return
							case <-tick.C:
								s.WriteToUDP([]byte("one-way-download"), target)
							}
						}
					}(a)
					continue
				}
				if p == 9003 {
					if n < 12 {
						continue
					}
					out = append([]byte(nil), buf[:n]...)
					out[2] = 0x81
					out[3] = 0x80
					out[6] = 0
					out[7] = 1
					out = append(out, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 127, 0, 0, 42)
				}
				s.WriteToUDP(out, a)
			}
		}(p, s)
	}
	select {
	case <-ctx.Done():
		server.Close()
		tlsServer.Close()
		for _, s := range udps {
			s.Close()
		}
		return nil
	case e := <-errch:
		return fmt.Errorf("origin server: %w", e)
	}
}
