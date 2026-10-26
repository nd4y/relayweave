#!/usr/bin/env bash
set -euo pipefail
# Invoke on build-01 under /home/av/build/relayweave/build.lock. Own checkout only.
root=$(pwd)
go_image=golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61
go_args=(--rm --cpus=1 --memory="${GO_TEST_MEMORY:-700m}" -e GOMAXPROCS=1 -e GOFLAGS=-p=1 -e GOMEMLIMIT="${GO_TEST_HEAP:-384MiB}" -v "$root:/src" -v relayweave-go-mod:/go/pkg/mod -v relayweave-go-build:/root/.cache/go-build -w /src)
case "${1:-go}" in
  go) docker run "${go_args[@]}" "$go_image" sh -c 'go test -race ./... && go vet ./... && go build ./cmd/control-plane ./cmd/proxy-worker' ;;
  performance) docker run "${go_args[@]}" "$go_image" go test ./internal/worker -run=^$ -bench=BenchmarkAutomaticSelection -benchmem -benchtime=1s ;;
  native)
    test -n "${XRAY_TEST_ROOT:-}"
    test "$(sha256sum "$XRAY_TEST_ROOT/xray" | cut -d' ' -f1)" = 6771b2b2700dee175357024738ad579de1c3f2c49eec0257e737d5aa38d12fed
    docker run "${go_args[@]}" --add-host=origin.test:127.0.0.1 -v "$XRAY_TEST_ROOT:/native:ro" -e XRAY_EGRESS_TEST_BIN=/native/xray -e XRAY_EGRESS_TEST_VERSION=26.10.17 "$go_image" sh -c 'go test -race ./internal/worker -run TestReal -count=1 -v && go test -race ./internal/core -run TestXrayEgressSourceTCPUDP -count=1 -v'
    ;;
  database)
    # Intentionally public synthetic credentials; ephemeral container/network.
    name=rw-rollover-test-db-$$
    network=rw-rollover-tests-$$
    network_id=$(docker network create "$network")
    container_id=
    trap 'if test -n "$container_id"; then docker rm -f "$container_id" >/dev/null; fi; docker network rm "$network_id" >/dev/null' EXIT
    container_id=$(docker run -d --name "$name" --network "$network" --network-alias rollover-db --cpus=.5 --memory=128m --tmpfs /var/lib/postgresql -e POSTGRES_PASSWORD=synthetic-rollover-only -e POSTGRES_DB=rollover postgres:18.6-bookworm -c shared_buffers=16MB -c max_connections=24)
    for attempt in $(seq 1 30); do
      if docker exec "$name" pg_isready -U postgres -d rollover >/dev/null; then break; fi
      sleep 1
    done
    docker run "${go_args[@]}" --network "$network" -e ROLLOVER_TEST_DATABASE_URL=postgres://postgres:synthetic-rollover-only@rollover-db/rollover?sslmode=disable "$go_image" go test -race ./internal/control -run TestSubscriptionSnapshotTransactions -count=1 -v
    ;;
  ui)
    docker run --rm --cpus=1 --memory=800m -e NODE_OPTIONS=--max-old-space-size=512 -v "$root/web:/app" -w /app node:26-bookworm-slim@sha256:298992629d69bbd5b2b03ad57094d8e4fcb3a8eeb7c4e25d29de656519568913 sh -c 'npm ci --no-audit --no-fund && node collect-notices.mjs && npm run build'
    ;;
  browser)
    mkdir -p "$root/reports"
    docker run --rm --cpus=1 --memory=800m -e NODE_OPTIONS=--max-old-space-size=512 -e BASE_URL=http://127.0.0.1:4173 -v "$root/web:/app" -v "$root/reports:/reports" -w /app mcr.microsoft.com/playwright:v1.64.0-noble sh -c 'node node_modules/vite/bin/vite.js preview --host 127.0.0.1 --port 4173 > /reports/preview.log 2>&1 & server=$!; trap "kill $server" EXIT; npx playwright test rollover.spec.ts'
    ;;
  *) echo 'expected go|native|database|ui|browser|performance' >&2; exit 2 ;;
esac
