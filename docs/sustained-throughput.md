# Sustained throughput: design and validation plan

**English** | [Русский](sustained-throughput.ru.md)

Status: first implementation available; sustained WAN acceptance is pending.
The complete controller below remains a development contract. See
[automatic routing](automatic-routing-goals.md) and [validation](validation.md).
A high short speed-test result does not prove
continuous throughput, single-stream bonding or symmetrical upload performance.

## Own traffic and dual-ISP headroom

In automatic/throughput mode, configured ISP download capacities are starting estimates, not rate limits. Two 800 Mbps subscriptions should use 800 Mbps for each ISP and 1600 Mbps for aggregate download capacity. Configure upload capacity from the actual upstream service. Manual weights do not override these capacity estimates in automatic mode.

Fresh per-engine payload counters, including draining generations, separate RelayWeave traffic from other WAN use. Its own active transfers already contribute to the scheduling workload; counting all WAN traffic again as background load would penalize productive paths twice. The scheduler subtracts only time-aligned payload observations (at most three seconds old). Missing, stale, future or unrelated observations receive no subtraction. Encapsulation is not subtracted. A bounded saturation guard retains queueing headroom, and EWMA/deadband/slew limits smooth changes. Without router telemetry, profile quality and local workload continue to control allocation.

Worker allocation evidence exposes total `wan_utilization`, `background_utilization`, `own_download_bytes_per_second` and `own_upload_bytes_per_second`. They describe observations, not guaranteed spare capacity. A busy ISP is never assumed to have unlimited capacity because its traffic belongs to RelayWeave.

Capacity origins returning HTTP 429 or 503 trigger a bounded cooldown using `Retry-After` (1 minute to 1 hour, default 10 minutes). Automatic capacity attempts during that cooldown reserve no more bytes. Throttling does not fail route health or replace an existing speed estimate. The original reservation remains spent, and health checks continue independently. Worker engine status exposes `capacity_probe_http_status` and `capacity_probe_not_before`. Cooldown is per route; it does not assert that every exit IP or destination shares the same restriction.

For throughput testing, use multiple authorized origins, fixed concurrency and the same object set before/after. Record HTTP status, complete bytes and wall-clock time. A rate-limited origin is not evidence of proxy capacity. Application aggregate throughput needs multiple connections; one established TCP stream remains on one proxy/ISP. Router shaping, CPU and Ethernet capacity remain external constraints. Two 800 Mbps links provide at most 1.6 Gbps before overhead, not a promise of 1.6 Gbps useful payload.

Capacity discovery includes eligible, already running reserves outside the selected hot window. It does not create additional engines or consume client-selection credits. Disabled, expired, revoked, staged, unready, circuit-open and target-unverified routes remain excluded, as do subscription replacements that have not completed canary admission. The existing serialized probe scheduler and durable traffic cap still apply; discovery does not guarantee a measurement for every subscription profile.

## Implemented first stage

Automatic profile/IP-group and ISP allocation now distinguish individually
observed quiet TCP sockets from transferring/new/unobserved work. A socket with
no recent byte transfer after its initial setup retains a small0.05 load cost;
new connections, UDP and stale observations retain full load. Close removes the
idle observation so it cannot discount a later connection. This is a network
activity heuristic, not application-level traffic classification.

Worker status exposes direction-specific10/60second exponential rate averages
and the actual load operands. A stale active measurement can serve as an explicitly
labelled historical prior for up to24hours, capped at the unknown100Mbps prior.
It does not become a fresh measurement or grant health/admission. A previously
slow measured path must not become faster solely because its sample expired.
Encrypted restart hints still expire at15minutes; this change does not extend
that persistence TTL or refund/increase probe reservations.

`cmd/relayweave-bench` is an opt-in workload generator, not a background probe:

```sh
# Build on the build host, then run on the actual measurement host.
go build -o relayweave-bench ./cmd/relayweave-bench
export RELAYWEAVE_BENCH_PROXY=http://127.0.0.1:18080
export RELAYWEAVE_BENCH_DOWNLOAD_URL=https://speed.cloudflare.com/__down
export RELAYWEAVE_BENCH_UPLOAD_URL=https://speed.cloudflare.com/__up
./relayweave-bench --duration=30m --max-bytes=412316860416 \
  --chunk-bytes=67108864 --parallel=4 --adaptive >download.jsonl
./relayweave-bench --duration=3m --max-bytes=42949672960 \
  --chunk-bytes=8388608 --parallel=4 --adaptive --upload >upload.jsonl
```

These example caps permit substantial traffic; every invocation requires an
explicit cap. Endpoints and proxy credentials are environment-only and excluded
from output. HTTPS verification stays enabled. Redirects and compressed download
responses are rejected. No target defaults or automatic invocation are provided.
HTTP/HTTPS/SOCKS5 proxy URLs are supported; an empty proxy setting is an explicit
direct baseline. `--allow-http` is for isolated synthetic HTTP fixtures.

The generator streams/downloads to discard and uploads generated synthetic data.
Its concurrency search increases4/8/16 at bounded30second steps, falls back on a
plateau or relative-latency regression and stops searching. This changes only the
generator's own concurrency; a proxy cannot manufacture parallel connections for
an unrelated application. Requests keep their chosen path until completion.

JSONL separates valid completed bytes, attempted payload bytes, conservative
application-byte reservations, failures and latency sample count. Its10second
rate distributions describe payload transfer; valid average throughput excludes
incomplete/invalid transfers. Transport overhead is outside the application-byte
cap. Requests cut by the workload deadline can leave partial payload in the
attempted count. A byte-cap stop is distinguished from a full-duration result.

## First sustained WAN result

The first isolated dual-ISP run on 2026-10-23 (Europe/Moscow), using runtime
source `12215bc01224b5ed9a1c27fcf7e7a169b59c49cd`, **failed acceptance**.
The workload ran for the full 1800 seconds. This is an experimental result,
not a stable-throughput claim or an accepted production rollout.

| Measurement | Result |
| --- | --- |
| Valid completed download average | 183.13 Mbps |
| Valid completed bytes | 41,204,842,496 |
| Completed bulk transfers | 614 |
| Attempted payload average | 195.12 Mbps |
| 10-second attempted-payload median / p05 | 184.22 / 12.63 Mbps |
| Request failures, bulk and latency probes combined | 2694 |
| Successful end-to-end latency p95 | 497 ms |

Both ISP paths carried observed traffic, but before/after counters from only
currently live cores lose retired-core traffic and cannot establish precise ISP
shares. Short 20-second per-ISP checks had zero request failures: download/upload
228.16/19.29 Mbps on one path and 307.02/23.91 Mbps on the other. These short
checks do not establish sustained upload performance. The planned three-minute
upload was skipped when the supervisor rejected the download result.

The generator does not yet classify failure causes; the aggregate count must not
be described as failed files or attributed to a specific network component.
The origin, workload and route selection differ from a browser speed test, so
the results are not a controlled comparison with that test. Active capacity
probes were disabled in the isolated run. Production service was preserved and
the experimental container was removed without OOM or restarts.

Go race tests and vet passed on the build host. Real-core checks covered both
bundled versions; an initial native timeout remains recorded alongside the
controlled passing follow-up. These checks do not override the failed WAN run.
Error classification, bottleneck diagnosis, controlled fixed-path comparisons
and sustained upload validation remain required before performance acceptance.

## Dual-800-Mbps follow-up (2026-10-25)

The worker changes passed `go test -race ./...`, `go vet ./...` and real-core
HTTP/SOCKS TCP/UDP and subscription-rollover checks with Xray 26.10.17 and
26.9.30. The final worker was deployed; external HTTP CONNECT, SOCKS5 TCP and
SOCKS5 UDP/DNS checks passed. The restart preserved the exhausted 64 MiB capacity
share of the unchanged 128 MiB daily budget. Reserve discovery therefore passed
automated admission tests but could not obtain new automatic WAN samples that day.

**The 1.5 Gbps performance objective was not achieved or accepted.** A native
Windows client with a 2.5 GbE link downloaded the same 64 objects (31,625,365 bytes
each), from four HTTPS test origins, with 16 concurrent requests and verified TLS.
The legacy-worker baseline completed all objects at 873.7 Mbps. The first new
worker run completed all objects at 748.8 Mbps. The final worker completed all
objects at 522.9 Mbps. These are useful completed bytes divided by whole-batch
wall time, including setup and tails, not instantaneous peaks or a controlled
software-only comparison: route populations, learned estimates, configuration
and external capacity changed between runs.

A 32-connection/128-object trial delivered 903.3 Mbps in complete objects but
only 116/128 completed, with 12 timeouts. A later four-active/two-reserve run
completed 59/64 at 590.4 Mbps. Neither is a stable-throughput acceptance result.
A direct dual-ISP test without external proxies completed only 38/64 objects;
it cannot establish the WAN ceiling. Rate-limited runs were excluded rather
than counted as evidence of proxy capacity.

Final upload validation completed and server-confirmed all 32 synthetic 1 MiB
objects at 23.1 Mbps; an earlier matching run reached 36.7 Mbps. A larger upload
trial timed out and produced an incomplete log, so no aggregate speed was accepted
from it. Sustained upload, sustained gigabit download and a software-only A/B
remain unverified. Router shaping and CPU pressure were observed separately;
router settings were preserved. These results must not be presented as a speedup.

## Objective

Maximize delivered application bytes over time while respecting a latency SLO,
protocol correctness, established-session continuity and explicit resource and
traffic budgets. Optimize download and upload separately. Show attainable
capacity and confidence, not just the highest one-second number.

Measure the limiting path before tuning: client NIC/Wi-Fi, LAN switches,
worker NIC/CPU/memory, local ISP, remote server-IP group, proxy transport and
destination. A roughly 940Mbps TCP result can already approach a 1Gbps Ethernet
client's practical limit. Two WANs cannot increase that client's Ethernet speed.
Use a faster LAN client or several clients to demonstrate higher aggregate rates.
Configured ISP capacities are priors, not measurements of either WAN.

## Control loop

1. Observe delivered bytes, active transferring connections, connection setup,
   failures and available TCP retransmission/RTT signals. Keep independent
   observations per `(profile, checked server-IP group, ISP, core)` and separate
   direction. Do not classify an application or an HTTPS request from ciphertext.
2. Evaluate speed over short and long windows, for example 1/10/60seconds.
   Missing/stale samples and counter resets create gaps. Distinguish an idle
   path, an underloaded lower bound and a demonstrably saturated path. Give
   evidence a timestamp/confidence; do not turn a stale known-slow path into an
   apparently fast unknown solely because an active sample expired.
3. Allocate NEW connections using estimated service capacity and current work.
   A long active transfer should count differently from an idle tunnel. Group
   aliases with overlapping checked server IPs before assigning capacity; adding
   aliases must not multiply the same balancer's apparent bandwidth. Different
   IPs are a preference, not proof of independent capacity.
4. Increase a promising allocation gradually; reduce it faster on sustained
   queueing, errors or independently confirmed degradation. Apply bounded changes
   and hysteresis to avoid moving every new connection between ISPs together.
   Do not infer an upper capacity bound from one slow destination/client.
5. Use fresh optional router telemetry to account for OTHER household traffic,
   link state and shared WAN utilization. Stale/missing/reset telemetry falls
   back to worker observations without disabling routing. This controller does
   not reconfigure provider links, routing, NAT or queues on the router.
6. Preserve healthy established TCP and authorized UDP sessions. New verified
   paths take new admissions; retired paths drain. A degraded existing bulk TCP
   connection is not transparently moved to another proxy or ISP.
7. Learn primarily from user traffic. Independent confirmation/exploration stays
   serialized and within durable probe budgets. An exhausted budget must remain
   exhausted across restarts. Never run perpetual large speed tests to keep the
   capacity estimates fresh.

These are development requirements. The existing selector still uses a
connection-count load approximation and heuristic priors; this document does not
claim the complete loop is deployed.

## Workload and measurement

Keep separate experiments for the actual client's experience and aggregate WAN
capacity. A generator on the NAS bypasses the client's LAN bottleneck and cannot
alone prove performance from the workstation. A build-host fixture cannot prove
home-WAN speed. Build and validate the generator on build-01; a lightweight LAN
client measurement requires the real client environment.

Use authorized origins with sufficient capacity and multiple independent origins
to identify destination throttling. For the repeat-download workload, retain the
same 100-object manifest and validate content/length. Stream benchmark payloads
without writing them to NAS storage; measure real file-writing performance as a
separate workload. Do not include retries as extra successfully delivered bytes.

Test 1,4,8,16 concurrent downloads and stop increasing concurrency when useful
throughput stops improving or latency worsens. Include both long-lived streams
and ordinary short transfers; a workload of only short requests can hide slow
long-stream behavior. Run interactive HTTPS requests concurrently and measure
end-to-end latency separately from the map's local connection-opening estimate.

Compare direct access where permitted, the best measured single proxy, and
automatic selection with identical origins, data and concurrency. Alternate run
order and repeat. First screen with short bounded runs; then confirm selected
settings for 10-30minutes. Record exact source/image/config identity. Do not select
the best instantaneous sample or discard a failed pair after seeing its result.

Every run MUST have a wall-clock deadline AND an explicit total byte cap covering
all concurrent bulk attempts/retries, plus bounded interactive traffic. Count
actual network consumption separately from valid delivered bytes. Estimate cost
before launching: at 942Mbps, 10minutes is about70.65GB and 60minutes about423.9GB
in decimal units, excluding protocol overhead. A soak that checks session
continuity is a different workload and need not consume bandwidth at this rate.

Record each second, with bounded buffering and no synchronous per-packet disk I/O:

- delivered upload/download, per-path/group/ISP allocation and sample confidence;
- 10second-window speed distribution, p05/median and time below the target;
- end-to-end interactive p50/p95/p99, timeouts, retries and transfer failures;
- worker/core CPU, CPU throttling, RSS, OOM and cgroup CPU/memory/I/O pressure;
- host pressure, NIC drops/errors, router WAN utilization when available;
- route-change reason, budget reservation state and preserved-session outcomes.

Save the trace before cleaning up owned fixture objects. Classify environmental
interference explicitly. A contaminated benchmark remains recorded as failed or
inconclusive; a controlled repeat is a new run with its own identity, not a silent
replacement of the failure. Do not stop foreign jobs to obtain a cleaner host.

## Acceptance and rollout

Agree thresholds before the run. Preserve the existing synthetic paired lower
95% confidence bound >=0.90 and interactive p95 <=0.2second for that existing
fixture. WAN targets must additionally account for the actual LAN ceiling and
idle end-to-end latency; they cannot be inferred from a synthetic fixture.

Accept the sustained controller only after repeated sustained results show no
meaningful throughput regression against the best fixed path, no latency-SLO
regression, no corrupted downloads and no unexpected established-session losses.
Include slowdown/recovery, shared-IP aliases, both enabled ISPs, absent/stale
router telemetry, exhausted probe budget and worker restart. Validate these
behaviors with real Xray transport, not only mocked weight distributions.

The UI should distinguish current rate, sustained rate, lower-bound capacity,
client/LAN ceiling and unknown causes. Never call a configured link rate a
measured speed or claim that an unexplained drop has been fixed.

## One file and multiple paths

RelayWeave currently schedules connections. A downloader can split a suitable
file into parallel HTTP Range requests, allowing separate connections to take
different paths. It must validate range support, object identity (such as a
strong ETag/If-Range), chunk integrity and behavior when a server returns a full
200 response instead of206. Some origins bind sessions to one exit IP.

This is a downloader feature, not transparent TCP bonding. An ordinary HTTPS
CONNECT/SOCKS proxy cannot split an opaque encrypted request into HTTP ranges.
True single-stream multipath needs cooperating endpoints/tunnels and remains a
separate architecture decision.

## References

- [ESnet iperf3](https://software.es.net/iperf/invoking.html): interval statistics,
  duration, reverse direction, parallel streams and machine-readable output.
  iperf3 alone does not validate the HTTP/SOCKS proxy path or web workload.
- [Linux network scaling](https://docs.kernel.org/networking/scaling.html):
  RSS/RPS/RFS/XPS are diagnostic/tuning tools when packet processing is the
  measured bottleneck. Do not enable unrelated tuning blindly.
