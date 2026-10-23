package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"proxyplatform/internal/compute"
	"proxyplatform/internal/identity"
	"syscall"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "compute_worker_failed")
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(os.Args) > 1 && os.Args[1] == "evaluate" {
		return compute.EvaluateProcess(ctx, os.Stdin, os.Stdout)
	}
	f := flag.NewFlagSet("compute-worker", flag.ContinueOnError)
	address := f.String("dispatcher", "", "HTTPS dispatcher URL")
	dir := f.String("identity-dir", "", "dedicated compute identity directory")
	if e := f.Parse(os.Args[1:]); e != nil {
		return e
	}
	if *dir == "" || *address == "" {
		return compute.ErrInvalid
	}
	if e := compute.EnforceLimits(); e != nil {
		return e
	}
	config, e := identity.ClientTLS(*dir)
	if e != nil {
		return e
	}
	config.ServerName = "compute-dispatcher"
	client, e := compute.NewClient(*address, config)
	if e != nil {
		return e
	}
	client.IdentityDir = *dir
	e = client.Run(ctx, compute.ExecuteChild)
	if ctx.Err() != nil && errors.Is(e, context.Canceled) {
		return nil
	}
	return e
}
