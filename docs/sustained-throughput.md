# Sustained throughput: design and validation plan

Status: first implementation available; sustained WAN acceptance is pending.
The complete controller below remains a development contract. See
[automatic routing](automatic-routing-goals.md) and [validation](validation.md).
A high short speed-test result does not prove
continuous throughput, single-stream bonding or symmetrical upload performance.

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
