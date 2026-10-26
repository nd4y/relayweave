# Dual-ISP queue and QoS experiments, 2026-10-25

**English** | [Русский](dualwan-ab-20261025.ru.md)

The best measured production download was **1.222 Gbps** for a complete batch,
with four WAN queues temporarily disabled. A separate repeat reached
**1.139 Gbps**. The **1.5 Gbps target remains unmet**, and these short experiments
do not establish sustained performance. All router changes were rolled back.

## Environment and method

- RelayWeave source: `f855398d306eb991dd95edebbff5bb02d1560196`.
  Production image:
  `sha256:96bcd8326aa9d5a7348cdb91cdfc91a8d17adce486bf15d88557b10d765c8024`.
  The production worker was not restarted or reconfigured during these tests.
- Two nominal 800 Mbps ISPs. MikroTik hAP ax3 with a negotiated 2.5 Gbps LAN
  trunk and a 1 Gbps port for each WAN. Existing CAKE types specify 760 Mbps
  per direction per ISP, with link overhead configured.
- Measurements ran from the Windows client PC2 through the production HTTP
  proxy. The isolated diagnostic binary was built on build-01. Production
  credentials stayed on the NAS; the public report contains no subscription data.
- The refined download workload used four public HTTPS benchmark origins,
  512 objects of 7,907,740 bytes, 32 concurrent transfers, a 30-second overall
  deadline and a 60-second per-transfer limit. TLS verification remained enabled.
  Bodies were discarded. A complete batch is 4,048,762,880 application bytes.
- Reported throughput counts only HTTP 200 responses with a successful curl
  exit and the exact expected length, divided by wall-clock time including
  startup and tails. This is a completed-object rate, not a payload hash check.
  Partial transfers at the overall deadline are excluded. No reported transfer
  errors does not mean that every scheduled object finished.
- Router CPU and WAN counters were collected concurrently. Wire counters
  include overhead and other traffic; they are not client goodput. The production
  selector remained automatic, so changing routes and origins limit causal claims.

## WAN queue A/B

Only the four existing ISP download/upload queue-tree entries were disabled
temporarily. Queue types, other queues, routing, NAT, firewall and provider
configuration stayed unchanged. An independently tested router-local timer and
the controller's cleanup both restored the original entries.

| Run | Original WAN queues | PC2 completed Mbps | Objects completed | Reported errors |
| --- | --- | ---: | ---: | --- |
| A5, one curl process | Enabled | 1044.8 | 496/512 | 9 connection timeouts |
| B3, one curl process | Disabled | 1222.4 | 512/512 | None |
| A6, four curl processes | Enabled | 1043.0 | 495/512 | None; deadline reached |
| B4, four curl processes | Disabled | 1139.4 | 512/512 | None |

B3 completed in 26.498 seconds; B4 in 28.427 seconds. These comparisons suggest
that the WAN queues contribute to the limit, but disabling them did not reach
1.5 Gbps. They do not justify leaving shaping disabled: sustained speed and
interactive latency under household load were not accepted by this experiment.

The client used about 0.51-0.55 CPU cores across the relevant curl processes.
Splitting the same concurrency across four processes did not reveal a client CPU
ceiling. Several router cores approached 100% under load; their average load
varied substantially between runs. CPU pressure is evidence of a constraint,
not proof that one router subsystem explains the entire throughput gap.

An earlier isolated fixed-profile fixture tested each ISP and both together:

| Fixture path | Original queues, Mbps | Queues disabled, Mbps |
| --- | ---: | ---: |
| ISP1 | 707.7 | 656.9 |
| ISP2 | 396.1 | 471.9 |
| Both | 868.0 | 1111.9 |

Those runs used larger objects and a 15-second per-object timeout, which caused
unfinished transfers. They are not measurements of the physical ISP ceilings.
Direct-access controls also depended on origin capacity and did not establish
800 Mbps availability for ISP2. Increasing concurrency or changing XHTTP XMUX
did not produce an accepted improvement. Later fixture restarts lacked complete
port/PID ownership evidence, so transport-specific causal claims are excluded.
All leftover fixture children were identified by their owned configuration
paths, stopped, and their private runtime files removed.

The isolated upload fixture sent 512 synthetic 1 MiB objects. Every upload had
a server-confirmed byte count. It measured 149.3 Mbps with original queues and
161.9 Mbps with those queues disabled. This is not a production upload acceptance
or a measurement of the ISP upload ceiling.

## Scoped QoS classification A/B/A

A separate experiment kept all original WAN queues enabled. Four temporary
`return` rules at the beginning of the application's QoS classification chain
matched only the two RelayWeave source addresses, in both directions. Existing
ISP selection, MSS handling, queue marking, NAT and security rules were preserved.
The chain's connection marks had no consumers in the other inspected mangle or
filter rules. A tested router-local timer removed the four rules after 180 seconds
if the controller failed; normal cleanup removed them earlier.

| Phase | QoS classification | PC2 completed Mbps | Objects completed |
| --- | --- | ---: | ---: |
| A, before | Original | 1013.7 | 481/512 |
| B | Scoped bypass | 994.8 | 472/512 |
| A, after rollback | Original | 973.3 | 462/512 |

All reported completed responses succeeded; the 30-second deadline left other
objects unfinished. Rule counters confirmed that the bypass matched traffic in
both directions on both ISP paths. The bypass result was almost equal to the
mean of the two controls. It did **not demonstrate a useful speed improvement**.
An earlier baseline measured 1017.8 Mbps; that sequence stopped before the B
workload because the verification script expected an omitted RouterOS field.
Cleanup restored the preimage, the verification was corrected, and the full
A/B/A above was run separately.

During the final series, whole-window WAN receive counters were approximately
708-798 Mbps on ISP1 and 409-466 Mbps on ISP2. Router core averages ranged from
71-87%, with individual samples reaching 100%. The catalog changed during the
measurements; worker payload deltas were calculated only for stable routes and
are lower bounds. Neither these counters nor one A/B/A prove an exclusive cause.

## Outcome and next decisions

The original queue tree, queue types, simple queues and canonical provider,
routing and firewall exports matched their pre-test state. Both temporary
rollback schedulers were removed. Owned test listeners and private fixture
configuration were removed. Final client checks passed HTTP CONNECT over HTTPS,
SOCKS5 TCP over HTTPS and SOCKS5 UDP DNS; HTTPS certificate verification succeeded.

The evidence narrows the next work to ISP2 path capacity, routing decisions and
router packet-processing cost. First compare fixed verified proxy+ISP paths with
direct controls against the same capable origins, using long enough transfers
to distinguish setup/tails from steady-state throughput. Preserve the weaker
and failed results. Only then compare a cheaper queue configuration or another
forwarding platform under both throughput and interactive-latency acceptance.
No permanent router change is selected by this report.

The configured CAKE bandwidth values sum to 1.52 Gbps per direction. That is a
configuration observation, not a verified application-throughput ceiling. Later
[fixed-path controls](path-capacity-20261026.md) measured more than 760 Mbps on
one ISP with these settings unchanged, so queue coverage and enforcement must be
validated before treating their sum as a hard limit. Adding proxy aliases or CPU
on remote nodes does not remove a shared forwarding constraint. Generic FastTrack
is not a safe shortcut for source-selected routing tables: RouterOS documents
its main-table restriction and queue/firewall bypass behavior in
[Packet Flow in RouterOS](https://help.mikrotik.com/docs/spaces/ROS/pages/328227/Packet+Flow+in+RouterOS).

See [sustained throughput](sustained-throughput.md) for the remaining acceptance
criteria. This report adds measurement evidence; it does not claim a new software
release, transparent single-stream bonding or a completed 1.5 Gbps optimization.
