# Local routing and optional compute v1

This branch implements the local-v1 baseline and the first bounded compute protocol from routing specification 2.0. It is a development candidate. It has not been deployed. Production acceptance requires the evidence listed below.

## Defaults and boundaries

An unchanged deployment uses the existing routing algorithm. Set `RELAYWEAVE_ROUTING_ALGORITHM=local-v1` on the data-plane worker to opt into the local estimator and cached selector. Manual, fallback, sticky and timed policies retain their existing code paths. TCP and UDP have separate plans. No GPU, media processing, interface/firewall changes, new probes, or changes to Tdarr process priority are performed.

The selector uses only already admitted routes and current permission/health/egress filters. Configuration, revocation, security lease and the final `RouteAllowed` check retain authority. Compute loss cannot extend the security lease. Existing flows are unaffected by recommendation expiry.

Local preparation runs once per second with active flows and once per ten seconds while idle. It groups endpoint aliases, sums their active sessions, takes the maximum observed rate and uses the least expensive healthy representative. Delivered rate is an observation, not a capacity estimate. An unavailable active measurement stays unknown. Additional history and per-byte wrappers are not installed.

## Development bootstrap

Build `cmd/compute-admin`, `cmd/compute-worker` and the existing control plane on the build host. Use a dedicated service user and a new directory for compute identity. The bootstrap command refuses an existing nonempty directory.

```sh
compute-admin -action bootstrap -identity-dir /var/lib/relayweave-compute/authority
# Supply COMPUTE_DATABASE_URL through the existing protected service environment.
compute-admin -action provision -identity-dir /var/lib/relayweave-compute/authority \
  -installation installation-1 -worker-id compute-1 -peer-dir /secure/staging/compute-1
```

Transfer only the peer directory through the existing secure administration channel. It contains `client.json` and `ca.pem`. The provisioning command is for Linux administration. On Linux, the worker's service user must own the peer directory and its 0600 private bundle; on Windows install the peer directory with an ACL restricted to its service account and system administrators. Never transfer authority `ca.key`, `signing.seed` or `token.key` to a compute worker. Provisioning explicitly pins the client's public key in the allowlist. A second certificate with the same name and another key cannot impersonate it.

Set `RELAYWEAVE_COMPUTE_CONFIG` on a development control plane to a protected JSON file:

```json
{"version":1,"enabled":true,"installation":"installation-1","listen":"127.0.0.1:9911","identity_dir":"/var/lib/relayweave-compute/authority","signing_seed_file":"/var/lib/relayweave-compute/authority/signing.seed","token_key_file":"/var/lib/relayweave-compute/authority/token.key"}
```

Use an explicit private/VPN address for a remote worker. Wildcards, public addresses and implicit hostname binds are rejected. No firewall/VPN configuration is installed by this feature. Absence of this variable creates no compute listener, migration or compute loop.

On the data-plane worker, install `recommendation-keys.json` through the trusted administration channel, then explicitly configure:

```sh
RELAYWEAVE_ROUTING_ALGORITHM=local-v1
RELAYWEAVE_COMPUTE_INSTALLATION=installation-1
RELAYWEAVE_COMPUTE_KEYS=/etc/relayweave/recommendation-keys.json
RELAYWEAVE_COMPUTE_MODE=shadow
```

`shadow` verifies recommendations but does not route traffic with them. The development `canary` mode uses a 1% cohort with a 0.10 recommendation blend; it must not be enabled in production before the specified acceptance gates. Offload requires at least two groups with sufficient outcomes and fresh active measurements. It preserves ISP shares because this implementation has no independent ISP capacity measurement. Unknown demand does not create a capacity claim.

For 0 workers, use only local-v1. For 1 worker, run one constrained outbound client with `examples/compute/compose.yaml`. For 2, add `-f examples/compute/compose.two.yaml` after the base compose file and provision the second identity separately. A nearby worker uses the same protocol as a remote worker. The global budget is shared, not doubled per policy or worker.

## Protocol and persistence

Protocol major is 1 and objective is `objective-v1`. The Go structs in `internal/compute/protocol.go` and `plan.go` are the authoritative wire contract. Messages use bounded ASCII integer-only canonical JSON; duplicate/unknown keys, excessive depth/dimensions, unsafe integers and compression are rejected. SHA256 digests and Ed25519 signatures bind installation, executor, HMAC policy/group IDs, executor boot, snapshot sequence, configuration generation, owner epoch and candidate range. Workers receive no subscriptions, endpoint/destination addresses, credentials or commands.

`POST /compute/v1/register`, `/heartbeat`, `/lease`, `/result`, `/cancelled`, `/renew` require dedicated TLS 1.3 mTLS identities. The client performs outbound requests only, rejects redirects and does not accept insecure certificate verification. A result capability goes only in the Authorization header. Capabilities are generated with the standard HMAC-SHA256 PRF from a protected key and unique attempt/session/task identifiers; only their hashes are persisted. This permits an idempotent lease response without storing plaintext capabilities.

`internal/compute/schema_v1.sql` is an additive, opt-in migration confined to compute tables. Ownership uses PostgreSQL time, a 10-second lease, 3-second renewal and an epoch fence. Dequeue and budget reservation commit together. Completed result rows form the durable publication outbox. Publication independently verifies and signs under the current installation row lock before committing; a failed transaction never publishes the envelope. A local executor verifies the signature and candidate again. A result is usable for at most 30 seconds from local capture using Linux CLOCK_BOOTTIME or Windows GetTickCount64. Remote wall clocks cannot refresh it.

Each child has a 500 ms CPU reservation, cooperative cancellation at 350 ms and a wall deadline no later than the task epoch. Process-tree limits are 1 CPU/512 MiB with one child. The global ledger caps rolling reservations at 1 CPU-second/10 seconds, 3 CPU-hours/UTC day and 64 MiB/day of application HTTP bytes. Unknown outcomes spend the full reservation and keep their slot quarantined for a conservative 62 seconds after expiry. These are reserved application bytes; they are not a measured wire-byte limit. Linux requires cgroup v2 limits; unsupported layouts fail closed. Windows uses a separate [Job Object with CPU hard cap](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_cpu_rate_control_information). Native tests verify kernel limits, inheritance, a real evaluator and kill-on-close; sustained CPU/memory boundary stress remains an acceptance gate.

## Stop, revoke, renew and restore

- Create `<RELAYWEAVE_COMPUTE_KEYS>.disabled` locally to clear recommendations and stop exports on the next one-second tick without waiting for the database. Remove the marker only after correction. Remove compute environment settings at the next controlled restart for a permanent off state.
- Revoke a worker with `compute-admin -action revoke -identity-dir ... -installation ... -worker-id ...`. The allowlist is checked on every HTTP request, including requests on existing TLS connections. Three invalid results in ten minutes revoke that worker until explicit operator reprovisioning. Malformed/duplicate/unknown JSON, invalid result values, missing capabilities, oversized bodies and forbidden compression all count after identity approval. Partial uploads and stale attempts do not count. Counter and audit commit together; an accounting failure returns 503. Disconnecting after a complete invalid upload does not cancel its bounded accounting operation.
- Leaf certificates last seven days. The worker renews the same key when at most 48 hours remain; the dispatcher renews its leaf locally. Renewal cannot change the pinned public key. Windows renewal uses [ReplaceFileW to retain the existing file ACL](https://learn.microsoft.com/en-us/windows/win32/fileio/moving-and-replacing-files). Native tests verify mTLS renewal, unchanged private key and protected DACL; crash/power-loss durability remains a gate. Reissue expired credentials through the administration channel.
- Recommendation public key changes are loaded every five seconds. Installing a changed trusted key set clears cached plans. There is no automatic trust in a public key carried by a recommendation. Perform overlapping key distribution and executor acknowledgement through the existing trusted operational process before signing with a new key.
- Before starting compute after restoring or losing its database ledger, run `compute-admin -action uncertain -identity-dir ... -installation ...`. This fences the owner and disables compute through a complete new UTC day. The local algorithm remains available. Restoring all persistent authority cannot be detected reliably from that same restored database; this step is a mandatory restore procedure.
- Binary rollback: stop only the compute service/container, replace it with the previously verified build, then register a new session. No Tdarr or data-plane restart is required for compute rollback. Restore legacy routing separately by removing the local-v1 opt-in during the usual worker update procedure.

## Verification and release limits

All builds and Linux tests run on build-01 under the shared project build lock with bounded container resources. The repository has functional/race tests, parser fuzz seeds, an independently generated canonical/candidate/Ed25519 vector in `internal/compute/testdata`, PG integration tests and mTLS scope/revocation tests. PG tests require `RW_COMPUTE_TEST_DATABASE_URL` pointing to a disposable fixture; they create and remove unique schemas. Child/real CLI tests require `RW_COMPUTE_WORKER_BINARY`. The Windows-only native tests are cross-built on build-01 and run as short separate Windows processes with `RW_WINDOWS_NATIVE_GATE=1`; only their helper joins its own constrained Job Object. Synthetic identities and ACL changes stay inside the test directory. Administrative CA bootstrap remains Linux-only. These tests verify boot-clock/deadline behavior but do not simulate physical host suspend/resume. `RW_RESOURCE_GATE=1` runs a paired 20-minute selector comparison (10 minutes for each arm) at 1000 selection attempts/s with 256 catalogue routes and 16 prepared routes, including p99. `RW_RESOURCE_IDLE_GATE=1` measures ten minutes of absolute idle CPU as a conservative upper bound on its increment. These run without race instrumentation. They do not simulate actual socket traffic. A failed earlier absolute CPU measurement is preserved with its source hash; it is not a successful resource gate.

Do not infer full specification acceptance from unit tests. Remaining release evidence includes idle/load/p99 resource gates on the final source, resume/clock and supervisor stress on both target OSes, actual socket/foreground/Tdarr nonregression, network/wire accounting, the complete fault matrix and a 24-hour shadow campaign. Production deployment requires explicit operator authorization and completion of the applicable gates. Authorization to deploy does not waive the acceptance requirements for enabling local-v1 or compute.

Current differences from the complete 2.0 design must remain visible: snapshot numeric fields cover the latency/reliability objective and fresh active rate only, unknown values use zero sentinels in this version; no ISP capacity optimiser, offline batch admission, contextual learning, GPU work, new user-flow exploration admission, production quality experiment or automatic staged rollout is supplied. The current finite search is deliberately small: if measured local search costs at most 10 ms and finds the same answer, retain local-only or use remote workers for offline validation. Remote availability alone is not evidence of useful offload.
