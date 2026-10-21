# Validation and scope

The source is a prototype, not a claim of universal provider compatibility or faster networking.
The current UI passed15 acceptance scenarios on an isolated Docker build host, including
IP grouping/expansion, recorded selection reasons, real ELK worker lifecycle/fallback,
SSE viewport stability, speed/rate reset and gap calculations, RTT median, card clipping,
mobile keyboard/fallback, fullscreen/Escape/menus, client exports and worker API reconnect.
Compact cards were checked at1920x1080 and3592x1956; four IP rows fit vertically at100%.

Heavy Go race/vet and frontend checks run on a build host using the pinned toolchains.
The GitHub workflow only performs lightweight repository/Python syntax checks.
Do not interpret its green result as a new runtime, UDP, security or throughput benchmark.

No real subscription links, device/HWID headers, filled environment files, private stack
snapshots, download manifests or production screenshots belong in this repository.
The README map screenshot uses synthetic RFC5737 addresses and a test worker.

The runtime architecture has prior disposable TCP/UDP and production acceptance, but
source publication does not re-establish provider availability or demonstrate native
ARM performance. This release does not bond one TCP flow across multiple proxy paths.

## Initial publication checks (2026-10-21)

- Pinned Go1.27.2: go test -race ./... and go vet ./... passed on the build host.
- TypeScript/Vite build and15 UI acceptance scenarios passed for the deployed frontend.
- Gitleaks v8.30.1 found no secrets in the curated public source; repository/Python syntax checks passed.
- Public Compose resolved successfully. A fresh isolated stack with empty data and the
  previously accepted runtime/frontend images passed UI/CSP/password-login/topology checks.
  This checks Compose bootstrap, not a fresh rebuild of every image or a WAN benchmark.
- Production frontend-only rollout preserved worker/control and unrelated container identities;
  private-network HTTP/SOCKS5TCP and UI HTTPS checks passed.

The screenshot is a synthetic fixture and is not used as a throughput measurement.
