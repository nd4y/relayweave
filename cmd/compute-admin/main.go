package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"proxyplatform/internal/compute"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "compute_admin_failed")
		os.Exit(1)
	}
}
func run() error {
	f := flag.NewFlagSet("compute-admin", flag.ContinueOnError)
	action := f.String("action", "", "bootstrap, provision, revoke, uncertain, public-keys")
	dir := f.String("identity-dir", "", "dedicated compute authority directory")
	peerDir := f.String("peer-dir", "", "new peer directory, provision only")
	id := f.String("worker-id", "", "opaque worker ID")
	installation := f.String("installation", "", "installation ID")
	output := f.String("output", "", "public key file, public-keys only")
	if e := f.Parse(os.Args[1:]); e != nil {
		return e
	}
	switch *action {
	case "bootstrap":
		return compute.Bootstrap(*dir)
	case "public-keys":
		if *output == "" {
			return compute.ErrInvalid
		}
		return compute.ExportPublicKeys(*dir, *output)
	}
	if !compute.ValidID(*installation) {
		return compute.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := os.Getenv("COMPUTE_DATABASE_URL")
	if url == "" {
		return compute.ErrInvalid
	}
	db, e := pgxpool.New(ctx, url)
	if e != nil {
		return e
	}
	defer db.Close()
	key, e := compute.ReadKey(filepath.Join(*dir, "token.key"))
	if e != nil {
		return e
	}
	r := &compute.Repository{DB: db, Installation: *installation, Owner: "compute-admin", TokenKey: key}
	if e = r.Migrate(ctx); e != nil {
		return e
	}
	switch *action {
	case "provision":
		return compute.Provision(ctx, r, *dir, *peerDir, *id)
	case "revoke":
		if !compute.ValidID(*id) {
			return compute.ErrInvalid
		}
		return r.Revoke(ctx, *id)
	case "uncertain":
		return r.Uncertain(ctx)
	}
	return compute.ErrInvalid
}
