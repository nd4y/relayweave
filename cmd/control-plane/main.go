package main

import (
	"context"
	"flag"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"proxyplatform/internal/control"
	"proxyplatform/internal/identity"
	"proxyplatform/internal/store"
	"proxyplatform/internal/telemetry"
	"proxyplatform/internal/wire"
	"regexp"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if e := run(); e != nil {
		slog.Error("control command failed", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	telemetry.Setup(ctx, "control-plane")
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "init" {
		if e := identity.Init("/control-identity", "/worker-identity"); e != nil {
			return e
		}
		for _, dir := range []string{"/data", "/workerdata", "/control-identity", "/worker-identity", "/cores"} {
			if e := os.MkdirAll(dir, 0700); e != nil {
				return e
			}
			if e := filepath.WalkDir(dir, func(path string, d os.DirEntry, e error) error {
				if e != nil {
					return e
				}
				return os.Chown(path, 65532, 65532)
			}); e != nil {
				return e
			}
		}
		for path, uid := range map[string]int{"/observability/prometheus": 65534, "/observability/grafana": 472, "/observability/loki": 10001} {
			if e := os.Chown(path, uid, uid); e != nil {
				return e
			}
		}
		return nil
	}
	if command == "worker-init" {
		if os.Getuid() != 0 {
			return fmt.Errorf("worker-init requires the storage initializer user")
		}
		for _, dir := range []string{"/data", "/cores"} {
			if e := os.MkdirAll(dir, 0700); e != nil {
				return e
			}
			if e := os.Chown(dir, 65532, 65532); e != nil {
				return e
			}
		}
		return nil
	}
	if command == "enroll" {
		f := flag.NewFlagSet("enroll", flag.ContinueOnError)
		id := f.String("id", "", "worker ID")
		out := f.String("out", "/export", "output directory")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		if !regexp.MustCompile(`^worker-[a-z0-9][a-z0-9-]{0,40}$`).MatchString(*id) {
			return fmt.Errorf("worker ID must start with worker- and contain lowercase letters, digits or hyphens")
		}
		a, e := identity.Open("/control-identity")
		if e != nil {
			return e
		}
		bundle, e := a.Issue(*id, false, nil)
		if e != nil {
			return e
		}
		if e = identity.Save(filepath.Join(*out, "client.json"), bundle); e != nil {
			return e
		}
		if e = store.AtomicFile(filepath.Join(*out, "ca.pem"), a.PEM, 0644); e != nil {
			return e
		}
		if os.Getuid() == 0 {
			for _, path := range []string{*out, filepath.Join(*out, "client.json"), filepath.Join(*out, "ca.pem")} {
				if e = os.Chown(path, 65532, 65532); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if command == "restore" {
		f := flag.NewFlagSet("restore", flag.ContinueOnError)
		file := f.String("file", "", "encrypted backup file")
		replace := f.Bool("replace", false, "replace existing configuration; control plane must be stopped")
		if err := f.Parse(os.Args[2:]); err != nil {
			return err
		}
		password := os.Getenv("BACKUP_PASSWORD")
		if password == "" {
			password = os.Getenv("ADMIN_PASSWORD")
		}
		dir := os.Getenv("DATA_DIR")
		if dir == "" {
			dir = "/data"
		}
		restoreCtx, done := context.WithTimeout(ctx, 5*time.Minute)
		defer done()
		counts, err := control.RestoreOffline(restoreCtx, *file, password, dir, *replace)
		if err != nil {
			return err
		}
		slog.Info("configuration restored; explicit Apply required", "counts", counts, "deployment_identity_preserved", true)
		return nil
	}
	if command != "serve" {
		return fmt.Errorf("commands: serve, init, worker-init, enroll, restore")
	}
	s, e := control.New(ctx)
	if e != nil {
		return e
	}
	defer s.Store.DB.Close()
	tls, e := identity.ServerTLS(s.IdentityDir)
	if e != nil {
		return e
	}
	ln, e := net.Listen("tcp", ":9443")
	if e != nil {
		return e
	}
	rpc := grpc.NewServer(grpc.Creds(credentials.NewTLS(tls)), grpc.MaxRecvMsgSize(8<<20), grpc.MaxSendMsgSize(32<<20))
	wire.Register(rpc, s)
	httpListen := os.Getenv("CONTROL_HTTP_LISTEN")
	if httpListen == "" {
		httpListen = ":8080"
	}
	api := &http.Server{Addr: httpListen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("GET /metrics", s.Metrics)
	metricsMux.HandleFunc("GET /discovery/workers", s.WorkerDiscovery)
	metrics := &http.Server{Addr: ":9091", Handler: metricsMux, ReadHeaderTimeout: 5 * time.Second}
	errs := make(chan error, 3)
	go func() { errs <- rpc.Serve(ln) }()
	go func() { errs <- api.ListenAndServe() }()
	go func() { errs <- metrics.ListenAndServe() }()
	slog.Info("control plane started")
	select {
	case <-ctx.Done():
	case e := <-errs:
		if e != nil && e != http.ErrServerClosed {
			cancel()
			return fmt.Errorf("control listener failed")
		}
	}
	stop, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	api.Shutdown(stop)
	metrics.Shutdown(stop)
	rpc.Stop()
	return nil
}
