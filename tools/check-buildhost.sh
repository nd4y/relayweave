#!/usr/bin/env bash
set -euo pipefail
# Run in an isolated source checkout on a build host. No production fixtures.
docker run --rm --cpus=1 --memory=1400m -e GOMAXPROCS=1 -e GOFLAGS=-p=1 -e GOMEMLIMIT=800MiB -v "$PWD:/src" -v relayweave-go-mod:/go/pkg/mod -v relayweave-go-build:/root/.cache/go-build -w /src golang:1.27.2-bookworm sh -c 'go test -race ./... && go vet ./...'
docker run --rm --cpus=1 --memory=1300m -e NODE_OPTIONS=--max-old-space-size=768 -v "$PWD/web:/app" -w /app node:26-bookworm-slim sh -c 'npm ci --no-audit --no-fund && node collect-notices.mjs && npm run build'
