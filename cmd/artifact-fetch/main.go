// Native build helper: verifies a target-architecture artifact without executing it.
package main

import (
	"context"
	"flag"
	"log"
	"proxyplatform/internal/core"
)

func main() {
	lock := flag.String("lock", "/out/artifacts.lock.json", "manifest")
	version := flag.String("version", "26.10.17", "version")
	arch := flag.String("arch", "amd64", "target architecture")
	root := flag.String("root", "/out/cores", "destination")
	flag.Parse()
	if e := core.InstallForArchitecture(context.Background(), *lock, *version, *root, *arch, false); e != nil {
		log.Fatal(e)
	}
}
