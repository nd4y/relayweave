# Security and audit scope

Use explicitly selected private LAN/NetBird interfaces; the default bind is127.0.0.1. One administrator uses ADMIN_PASSWORD. Session cookies, CSRF/origin checks, optimistic versions and durable operations protect configuration changes. Serve the UI over HTTPS and set COOKIE_SECURE accordingly. Optional client HTTP/SOCKS passwords are separate listener credentials.

Source fetches validate resolved destinations, bound response sizes and strip credentials/device headers on cross-host redirects. Private source addresses require explicit permission. Independent engine probes do not carry subscription headers or admin cookies. Secrets are encrypted in storage with protected local key material. Keep DATA_ROOT/env private; backup encryption does not protect a compromised live host.

Workers use mTLS and encrypted recovery/revocation checkpoints. Failed rollout retains permitted last-good paths without broadening a newer revocation fence. Commands are worker-addressed; generations/replays/duplicate results have explicit handling. Runtime containers have no Docker socket, run non-root/read-only and have finite CPU/memory/PID/descriptor/UDP/process/probe budgets.

Executable provenance checks the allowlisted GitHub release source, architecture, archive SHA-256, extracted binary SHA-256 and exact Xray version. This does not establish independent upstream signing or vulnerability freedom. A build approval trusts executable code; assess its repository and release bytes. Runtime digest checks detect changed bytes before process start.

## Audit evidence and remaining review

The application compiler is pinned toGo1.27.2. Recorded application source govulncheck found no reachable advisories. Full frontend npm audit, including dev dependencies, found zero advisories on2026-10-19. These results apply to the scanned snapshots.

Frontend runtime moved from the old nginx1.29 branch to the official stable nginx1.30.6 image, pinned by multiarch digest. That release includes the2026-10-14 upstream security fixes. Node, Go and distroless build/runtime bases also have explicit digests. Image scans and browser regression checks must use these exact final images.

Optional official Xray26.9.30 was compiled withGo1.27.1. Its binary scan reported14advisories including older standard library/x/net/compress dependencies. This fixture exercises multiple-version compatibility; it is not the recommended default production core. Official origin does not waive legacy-version security review.

Included Xray 26.10.17 uses Go 1.27.2 and updated dependencies. Its stripped-binary scan conservatively reported unmaintained OpenPGP advisory GO-2026-5932. A follow-up govulncheck source analysis of exact upstream commit 92ccc939aaf4f8965f369cf0b142e005aeaa11d5, entry point ./main and Go 1.27.2 found zero reachable advisories and zero affected imported packages; the OpenPGP advisory remains only in a required module whose affected package is not imported. This resolves that specific reachability warning, rather than suppressing the scanner result. Every different engine/build still needs its own assessment. Final validation must also record the image/base-component audit.

Upstream reports: [Go HTTP/2 HPACK race](https://pkg.go.dev/vuln/GO-2026-6617), [unmaintained OpenPGP](https://pkg.go.dev/vuln/GO-2026-5932), [exact builtin Xray source](https://github.com/XTLS/Xray-core/tree/92ccc939aaf4f8965f369cf0b142e005aeaa11d5).
