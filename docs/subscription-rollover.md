# Automatic subscription rollover

The default journey is to add subscriptions. The automatic pool discovers their
eligible profiles; the worker chooses diverse proxy/ISP paths and allocation
weights from observed latency, useful delivered traffic, bounded capacity
samples, failures and congestion. Manual profile selection and weights are
advanced overrides. Router telemetry is optional. No automatic operation enables
an operator-disabled ISP.

## Subscription changes

An address-only rotation preserves all effective credentials, TLS/REALITY peer
identity, transport parameters and unknown configuration fields. Matching uses a
typed, private HMAC template, never display names or provider ordering. A unique
match preserves the profile ID and its exclusions. Equivalent groups with shared
credentials can rotate automatically only when no member is individually pinned,
excluded, disabled or overridden. Other safe independent groups still proceed.
An address or metadata change which leaves an explicit host/name/country filter
is held for that group instead of implicitly revoking its existing sessions.
Confirmed safe groups keep their own freshness while held records retain frozen
deadlines.

Changes to credentials, keys, TLS identity, transport or unknown effective fields
are held for review. The existing accepted paths remain available within their
authorization. Ordinary safe address changes require no Accept action. HTTP
source transport does not authorize unattended endpoint replacement. Approval of
an exception is bound to the exact pending snapshot and source version.

Invalid/empty/failed responses do not erase the catalogue or renew its freshness.
A partially parsed response cannot silently remove missing records. Safe parts
may progress while held records retain their frozen authorization deadline.
HTTP 304 preserves pending review and running rollover state.
While a snapshot is pending, refresh omits conditional validators, including
custom conditional headers, and reclassifies a fresh body through the current
rules. Even an unsolicited 304 cannot renew held authorization or replay an
approval from an older release.

## Admission and continuity

Each dial reserves an engine reference before leaving the admission lock.
Ordinary rotations keep established TCP tunnels, half-close behavior and entire
SOCKS UDP associations on the original engine. New destinations in an existing
UDP association keep the same upstream. New associations use the currently
eligible revision. HTTP forward requests do not reuse a draining upstream for a
new request.

New revisions warm up before a 5%, 20%, 50%, 100% progression of new admissions.
Each stage needs elapsed time and successful observed sessions. Low traffic
cannot promote a candidate merely because a timer expired. The first stage has
connection, admission-rate and byte-based admission limits. These stop new
admissions rather than interrupt an already accepted long transfer.

An ordinary disappearance from a complete source is established-only retirement,
not a security revocation. Disable, explicit policy exclusion, listener ACL or
credential changes, expired authorization and security revocations take priority
over continuation and rollback. They cannot be undone by a failed candidate or
an old generation. Existing TCP/UDP may be closed for such a restrictive action.

Rollover records, including the old effective configuration, are stored only in
the authenticated encrypted worker journal. Increasing a candidate's share waits
for a durable checkpoint. Restrictive changes apply before slow checkpoint I/O.
After restart the worker revalidates rather than restoring stale health evidence.
A worker crash cannot preserve operating-system TCP/UDP sockets.

## Health and security

TCP health means a real request through the proxy, not just an open local SOCKS
port. UDP health uses an actual DNS request/response through the complete path,
separately for each proxy/ISP pair. UDP and TCP readiness are independent.

The worker checks the full DNS answer and pins the actual Xray server socket to
a permitted literal IP. Original TLS server name and HTTP transport authority
are preserved. DNS endpoint replacement goes through revalidation and rollover.
Local, metadata and link-local targets stay blocked. Source `allow_private`
controls source retrieval only. A private proxy requires an explicit narrow
`proxy_allow_cidrs` exception; it never follows from permission to fetch a source
on a private network. Changes to this exception have their own admission fence.

Core artifacts remain digest-pinned. Subscription contents cannot install a
different executable or clear generation/admission/replay fences. Legacy workers
do not receive assignments requiring revision-aware routing; the admission guard
is projected restrictively for their older protocol.

## Weights and measurement costs

Rollover retains the hierarchical automatic scheduler: ISP allocation, shared
server-IP group, then profile. Optional fresh router telemetry adjusts ISP
allocation; missing telemetry preserves autonomous operation. Aliases sharing
remote IPs share capacity credit. See [router integrations](router-integrations.md)
for the actual weighting and freshness contract. Delivered goodput is a lower
bound, not a claim of total available capacity.
Interactive latency under load and aggregate bulk speed are separate objectives;
one established TCP stream is not bonded across proxies or ISPs.

New automatic policies use a 256 MiB daily probe payload reservation. At most half
is available to automatic throughput bursts; the remainder protects health.
The default bounded download endpoint is the
[Cloudflare speedtest download API](https://github.com/cloudflare/speedtest/blob/main/README.md)
with an 8 MiB response. RelayWeave does not use the SDK's result-upload APIs.
The operator can override the measurement target or set `disable_speed_probes`.
Existing explicit budgets remain limits; exhaustion delays exploration, and
passive observations continue to inform selection. Network framing/handshake
overhead is additional to a payload reservation.

No percentage improvement is promised. Acceptance compares the same workloads
against direct access where applicable, the best measured single proxy, a static
healthy candidate set and the prior release. Missing or stale router information
must not prevent baseline operation.

## Resource limits and long sessions

The process limit remains 32, including draining processes and candidates, with
at most two concurrently staged engines. Cgroup memory/PID and file descriptor
headroom are checked before creating another process. Active, in-flight and
probe references prevent collection. Artifacts of running/draining engines are
protected from cache cleanup.

Healthy authorized long sessions have no default forced maximum age. Limited
retired revision slots can delay the next rollover; intermediate unadmitted
observations are coalesced. An offline worker conservatively pins revision
history until its frozen deadline. Expiry or disappearance of the actual old
server is not a seamless migration guarantee.

## Operations and verification

Worker status exposes redacted rollover phases/reasons, admission percentage,
remaining old sessions, protocol readiness and automatically calculated weights.
`GET /api/v1/sources/{id}/rollovers` filters this view to a source. A queued or
successful catalogue operation is not the same as a worker activation or a fully
drained old revision.

For a new installation only, adding a source bootstraps an automatic pool,
policy and loopback-only HTTP/SOCKS listeners. Existing operator listeners are
not overwritten or broadened. Deploying a new binary remains a separate action.

Run the checks from an isolated checkout on the build host, under the project's
shared build lock:

```sh
flock -w 50 /home/av/build/relayweave/build.lock bash tools/check-rollover.sh go
XRAY_TEST_ROOT=/path/to/approved/test/core \
  flock -w 50 /home/av/build/relayweave/build.lock bash tools/check-rollover.sh native
flock -w 50 /home/av/build/relayweave/build.lock bash tools/check-rollover.sh database
flock -w 50 /home/av/build/relayweave/build.lock bash tools/check-rollover.sh ui
flock -w 50 /home/av/build/relayweave/build.lock bash tools/check-rollover.sh browser
```

The native check includes actual Xray TCP/UDP, a wall-clock staged rollover,
durable admission advancement and continuous old TCP/UDP sessions. The database
check creates only its own disposable database/network. Browser fixtures verify
automatic-stage explanations without a routine Accept button. Unit/race checks
cover semantic matching, grants, concurrency, retirement, resource budgets and
restrictive changes. Long production soak, physical multi-ISP throughput and a
production rollout are separate acceptance steps, not implied by these tests.
