package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"proxyplatform/internal/routertelemetry"
	"syscall"
	"time"
)

func main() {
	path := flag.String("config", "/config/router-telemetry.json", "read-only module configuration")
	health := flag.Bool("healthcheck", false, "check process liveness on loopback")
	flag.Parse()
	registry := routertelemetry.Builtins()
	cfg, err := routertelemetry.Load(*path, registry)
	if err != nil {
		log.Fatal(err)
	}
	if *health {
		c := http.Client{Timeout: time.Second}
		resp, e := c.Get("http://" + cfg.Listen + "/livez")
		if e != nil {
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	token, err := routertelemetry.ReadSecret(cfg.TokenFile)
	if err != nil || len(token) < 32 {
		log.Fatal("telemetry API token must contain at least 32 bytes")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	m := routertelemetry.New(cfg, registry)
	server := &http.Server{Addr: cfg.Listen, Handler: m.Handler(token), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Print("router telemetry started in observe mode")
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal("telemetry listener failed")
	}
	stop()
	<-done
}
