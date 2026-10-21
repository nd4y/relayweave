# Default automatic routing: product acceptance contract

Priority 1, specified by the owner on 2026-10-22: a user adds subscriptions and
gets the best practical speed, latency and stability without selecting profiles
or tuning weights. This document is an acceptance requirement, not a claim that
all of the following behavior is implemented in the current release.

## Normal user journey

1. Add one or more subscriptions. Validate and discover usable profiles.
2. Automatically perform bounded eligibility, connectivity, latency, protocol
   and useful-throughput checks. Do not make the user rank or select profiles.
3. Automatically select a diverse active candidate set and compute allocation
   weights from actual observations, confidence, saturation and failures.
4. Continuously adapt new connections to changing conditions; preserve healthy
   existing sessions. Explain current choices without requiring user decisions.
5. Automatically validate and stage routine subscription address rotations,
   warm the replacements, shift new connections and drain retired paths.
   A concrete security anomaly may require review; ordinary provider rotation
   should not routinely stop at a manual Accept button.

Profile selection and weights are automatic by default. Manual profiles and
weights are optional advanced overrides and must not be prerequisites for a
working deployment. Missing MikroTik or any other router integration must not
disable the baseline automatic behavior. Router telemetry improves household
congestion awareness but is an optional input with explicit freshness/fallback.

## Optimization behavior

Use interactive latency under load and sustained useful bulk throughput as
separate objectives. A connection with a good idle RTT can still be saturated;
one long transfer and many idle sessions are not equivalent load. Do not promise
that one scalar score can maximize every objective simultaneously. Protect
interactive traffic while using spare capacity for parallel downloads.

Select and evaluate independent (profile, ISP) paths. Prefer genuinely different
remote server IPs; subscription aliases behind a shared endpoint do not create
extra remote capacity. Select only operator-enabled, valid local ISP paths.
Automatic selection must never enable ISP1 or a physical WAN against an operator
restriction. Preserve explicit revocations, access restrictions and core limits.

Measure conservatively and distinguish unknown, idle and failed. Start with
safe priors, budget active tests, learn from delivered user traffic, explore
alternatives within bounded cost, use smoothing/hysteresis and avoid all paths
oscillating together. Retry/circuit-break a faulty path without poisoning every
profile merely because a shared local ISP is congested. Unsupported transports
or insufficient telemetry must have an explicit fallback.

## Required comparison matrix

On the build host, compare the automatic default with direct access where
applicable, the best measured single proxy and the previous released default.
Use the same data and concurrency for each comparison. Include interactive
requests during bulk transfer, mixed long/short flows, one versus several server
IPs, aliases sharing a balancer, proxy slowdown/failure/recovery, subscription
rotation and stale/absent router telemetry. Use one ISP when a second ISP is not
authorized for activation; report that limit rather than invent dual-WAN gains.

Report delivered bytes/time, success rate, p50/p95/p99 latency under load,
retries, interrupted established sessions, warmup/recovery time, probe traffic,
worker/router resource cost and exact source/image identity. Performance claims
require repeated real measurements and an explanation of the available path
capacity. A synthetic scheduler distribution test is not a throughput result.

Current automatic modes compute ISP and shared-IP profile weights using local
quality/load observations, optionally assisted by whole-WAN router telemetry.
Remaining limits include the session-count load approximation, heuristic
capacity priors and mandatory review of some subscription changes. Routine
subscription rollover and advanced traffic classification remain independent
acceptance work; do not call the entire product complete based on this selector.
