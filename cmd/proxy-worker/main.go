package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"proxyplatform/internal/config"
	"proxyplatform/internal/core"
	"proxyplatform/internal/lab"
	"proxyplatform/internal/telemetry"
	"proxyplatform/internal/worker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if e := run(); e != nil {
		slog.Error("command failed", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	telemetry.Setup(ctx, "worker")
	if len(os.Args) < 2 {
		return fmt.Errorf("commands: serve, install-core, lab-init, lab-origin, run-core, health, signal-core")
	}
	f := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	switch os.Args[1] {
	case "agent":
		return worker.AgentRun(ctx)
	case "serve":
		path := f.String("config", "/lab/worker.json", "configuration file")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		c, e := config.Load(*path)
		if e != nil {
			return e
		}
		m, e := core.New(c)
		if e != nil {
			return e
		}
		if e = m.Start(ctx, c); e != nil {
			return e
		}
		g := worker.New(ctx, c, m)
		if e = g.Start(); e != nil {
			cancel()
			m.Wait()
			return e
		}
		slog.Info("worker listeners started")
		<-ctx.Done()
		g.Close()
		m.Wait()
		return nil
	case "install-core":
		lock := f.String("lock", "/app/artifacts.lock.json", "artifact lock")
		version := f.String("version", "", "version")
		root := f.String("root", "/cores", "cache root")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		if e := core.Install(ctx, *lock, *version, *root); e != nil {
			return e
		}
		slog.Info("artifact verified", "version", *version)
		return nil
	case "lab-init":
		dir := f.String("dir", "/lab", "fixture directory")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		return lab.Init(*dir)
	case "lab-origin":
		dir := f.String("dir", "/lab", "fixture directory")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		return lab.Origin(ctx, *dir)
	case "lab-subscription":
		dir := f.String("dir", "/lab", "fixture directory")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		return lab.Subscription(ctx, *dir)
	case "lab-quic-client":
		proxy := f.String("proxy", "127.0.0.1:41080", "SOCKS5 address")
		user := f.String("user", "", "SOCKS5 username")
		password := f.String("password", "", "SOCKS5 password")
		ca := f.String("ca", "/lab/ca.pem", "verification CA")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		test, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if e := lab.QUICClient(test, *proxy, *user, *password, *ca); e != nil {
			return e
		}
		slog.Info("QUIC UDP roundtrip passed")
		return nil
	case "lab-shaper":
		listen := f.String("listen", ":2443", "test listener")
		target := f.String("target", "upstream:2443", "test upstream")
		control := f.String("control", ":9006", "test control")
		mbps := f.Int64("mbps", 200, "per-direction shared capacity")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		return lab.Shaper(ctx, *listen, *target, *control, *mbps)
	case "run-core":
		binary := f.String("binary", "/opt/xray/builtin/xray", "binary")
		path := f.String("config", "", "core configuration")
		prepare := f.Bool("prepare-lab-upstream", false, "allow only the exact isolated lab origin")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		if *prepare {
			if e := lab.PrepareUpstream(*path, "/tmp/upstream.json"); e != nil {
				return e
			}
			*path = "/tmp/upstream.json"
		}
		cmd := exec.Command(*binary, "run", "-config", *path)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if e := cmd.Start(); e != nil {
			return e
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case e := <-done:
			return e
		case <-ctx.Done():
			cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
				return nil
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				<-done
				return nil
			}
		}
	case "health":
		url := f.String("url", "http://127.0.0.1:9090/healthz", "URL")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		return worker.Health(*url)
	case "signal-core":
		id := f.String("id", "", "core ID")
		state := f.String("state", "/lab/state", "state directory")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		if *id != "primary" && *id != "secondary" {
			return fmt.Errorf("lab core ID required")
		}
		b, e := os.ReadFile(filepath.Join(*state, *id+".pid"))
		if e != nil {
			return e
		}
		pid, e := strconv.Atoi(strings.TrimSpace(string(b)))
		if e != nil || pid <= 1 {
			return fmt.Errorf("invalid PID")
		}
		cmdline, e := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if e != nil {
			return e
		}
		if !strings.Contains(string(cmdline), *state+"/"+*id+".json") {
			return fmt.Errorf("PID does not match core instance")
		}
		return syscall.Kill(pid, syscall.SIGKILL)
	default:
		return fmt.Errorf("unknown command")
	}
}
