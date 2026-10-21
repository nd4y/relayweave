# MeTube: parallel downloads with a stable upstream per file

The generic RelayWeave proxy balances connections. Some media/CDN requests need
the same egress throughout extraction and download. For those requests, use
authenticated internal listeners backed by independent server-IP groups and
the read-only `sitecustomize.py` integration from this directory.

The hook selects a healthy path with the lowest active load once per metadata
extraction or download. It sets the path before MeTube installs its existing
socket guard. Every subsequent request in that yt-dlp instance retains it,
including aria2 and concurrent HLS fragments. Independent files can use
different paths. An already established TCP connection is never migrated.

Each path policy uses a pool for one independently verified server host/IP group,
deterministic `fallback` selection, one active route and one reserve. Verify
source compatibility as well as the common health endpoint before adding a path.
These pools follow subscription refresh for the configured hostname; they are
not automatic discovery of every future hostname in a subscription. A shared
IP is only a conservative capacity hint; actual exit consistency still needs
live validation. Prefer hosts with consistent application egress.

Policies can have a target-specific HTTPS probe without a generic fallback.
The worker evaluates that target separately for each policy, so an otherwise
healthy core can be rejected for this workload without being quarantined for
other clients. HTTP CONNECT itself cannot inspect encrypted application errors.

Configuration in the official MeTube container:

```yaml
environment:
  PYTHONPATH: /opt/relayweave:/app/app
  RELAYWEAVE_DOWNLOAD_PATHS: '[{"port":8083,"policy_id":"path-a"},{"port":8084,"policy_id":"path-b"}]'
volumes:
  - /path/to/public/integration:/opt/relayweave:ro
```

`YTDL_OPTIONS.proxy` and `HTTP_PROXY` point at the authenticated internal base
listener `relayweave-proxy:8082`; keep the actual password in protected home-host
configuration. Each selected port must use the same credentials. Bind access
to the existing private Docker subnet, require authentication, and publish none
of the extra ports on the host or Traefik. The integration rewrites only this
specific base proxy; explicit other proxies are preserved.

Keep `MAX_CONCURRENT_DOWNLOADS=4`. Use aria2 HTTP/HTTPS options
`-x8 -s8 -j1 -k1M` and yt-dlp `concurrent_fragment_downloads=8` for native HLS.
The aria2 limit alone does not parallelize native HLS. These values are upper
bounds, not a guaranteed number of simultaneously useful connections.

Route status is fetched directly from the existing private worker status port,
with a two-second timeout, one-second cache and at most five seconds of cached
fallback. No healthy path causes a clear error, not an unverified direct
connection. Cross-path shared IPs use combined load in the selection heuristic.
An unhealthy path is omitted for new work; existing application requests are
not transparently replayed across identities. Full-job retry remains an
application concern. Target health, source identity and complete output media
must be checked in live tests; a successful homepage fetch alone is insufficient.

This small integration depends on MeTube's `dl_formats.get_opts` and yt-dlp's
constructor. After MeTube upgrades, verify `sitecustomize.HOOK_ACTIVE`, the
wrapped function, and a real download before declaring compatibility. Use the
exact deployed image and retain a Compose/config checkpoint for rollback.
The hook records no source URL, credentials, filename or payload.

`test_routing.py` exercises the selection contract using synthetic modules and
status only. Run it on the build host. Real-source tests and private manifests remain
on the home host; never upload them to the build server or public repository.
