# Fixed-path capacity and queue comparisons, 2026-10-26

**English** | [Русский](path-capacity-20261026.ru.md)

The **1.5 Gbps target remains unmet**. Direct controls confirm a shared router
processing constraint: disabling four WAN queues raised completed download from
871.8 to 1184.8 Mbps; restoring them returned it to 931.3 Mbps. This does not mean
that queues explain every limit. ISP2 paths and slow transfers through remote
proxies also need attention. All router changes in this report were temporary.

## Method and deployment identity

PC2 downloaded through st1; the router retained its provider configuration,
source-based ISP selection, NAT and firewall. Two direct controls used separate
HTTP listeners whose outbounds used an ISP-specific source address. They bypassed remote proxies while
still traversing the same router. A separate fixed-profile control used the
production Xray binary with one selected remote profile per path. Neither control
is the full RelayWeave gateway; production measurements explicitly used the
production HTTP listener and its automatic route selection.

Production source remained `f855398d306eb991dd95edebbff5bb02d1560196`; worker image
remained `96bcd8326aa9d5a7348cdb91cdfc91a8d17adce486bf15d88557b10d765c8024`.
The native control used the deployed Xray 26.10.17 binary. Its PID, listening
sockets and binary hash were checked before traffic. It accepted only the test
client and specified HTTPS origins, had a finite lease, and was cleaned up after
use. Direct controls contained no subscription credentials.

Each single-ISP batch contained 64 objects; combined and production batches had
128. Each object was 31,625,365 bytes, so a combined batch contained 4,048,046,720
payload bytes. Four curl processes provided 16 concurrent requests for a single
ISP and 32 for combined/production tests. Origin assignment and ISP assignment
were independent. Connections could be reused. Retries were disabled, TLS was
verified, and response bodies were discarded. Overall and per-transfer limits
were 60 seconds.

Completed goodput is the sum of exact-length HTTP 200 responses with curl exit 0,
divided by monotonic batch duration. Partial responses are excluded and failures
are retained. This is batch goodput, not sustained capacity or an instantaneous
WAN counter. Fixed byte allocation and slow final transfers can leave one path
idle before the batch ends. Payload length was checked; payload hashes were not.

## Sources and fixed paths

Some of the original four European origins answered HEAD but did not finish a
large response before the deadline. Those incomplete runs were retained and were
not interpreted as ISP ceilings. A two-origin mix, Melbicom Frankfurt and 23M,
removed the worst source tails for the next comparison, with original queues:

| Path | Completed Mbps | Objects completed |
| --- | ---: | ---: |
| Direct ISP1 | 534.0 | 64/64 |
| Direct ISP2 | 319.1 | 64/64 |
| Direct, both ISPs | 767.8 | 128/128 |
| One fixed proxy per ISP | 575.1 | 128/128 |
| Production automatic RelayWeave | 860.9 | 128/128 |

The automatic selector outperformed this particular manually fixed pair. This is
not proof of global optimality. Another profile+ISP combination failed HTTPS
preflight and was excluded from the throughput matrix. A failed gstatic probe
alone was not treated as failure to reach every origin.

Nearby FDC Moscow and ProCloud controls measured 833.1 Mbps on ISP1 and 622.1 Mbps
on ISP2, with all 64 objects complete in each case. Together they reached
948.7 Mbps, all 128 complete. The native Xray process used about 0.31 CPU cores,
while router core averages were 95.5%, 92.0%, 95.8% and 86.3%, with peaks near
100%. These observations do not establish an 800 Mbps physical ceiling for ISP2
or measure production gateway CPU demand.

## Original queues versus disabled queues

A fresh configuration snapshot and canonical fingerprints were saved. An
independent router-local rollback was tested before changes, then armed for
480 seconds. Only four WAN queue enabled flags changed during this series.
Provider settings, routes and firewall remained unchanged.

Direct controls used the same nearby-origin mix:

| Phase | Completed Mbps | Objects completed | Seconds |
| --- | ---: | ---: | ---: |
| CAKE, before | 871.8 | 128/128 | 37.15 |
| Queues disabled | 1184.8 | 128/128 | 27.33 |
| CAKE, restored | 931.3 | 128/128 | 34.77 |

With queues disabled, individual ISP tests gave 827.9 and 590.5 Mbps, all 64
objects complete. The combined test still loaded all router cores heavily
(average 96.8%, 87.0%, 85.6%, 80.9%; each peaked at 100%). Native Xray used
about 0.29 CPU cores. The WAN receive sum over the sampled interval was about
1472 Mbps, including protocol overhead and other traffic; it is not a 1.5 Gbps
application result.

Production RelayWeave used the same two-origin European mix in all three phases:

| Phase | Completed Mbps | Objects completed | Seconds |
| --- | ---: | ---: | ---: |
| CAKE, before | 956.3 | 128/128 | 33.86 |
| Queues disabled | 530.9 | 126/128 | 60.05 |
| CAKE, restored | 838.1 | 128/128 | 38.64 |

The disabled-queue production run had two curl timeouts and other slow final
transfers. Its lower batch average must not be hidden, nor does one variable
external run prove that disabling queues caused the failures. Both controls
completed all objects. The benchmark did not identify the exact failing
proxy+ISP paths, so no individual profile is blamed by this report.

## FQ-CoDel candidate

A second independent A/B/A changed only the four WAN queue entries to the
existing FQ-CoDel type plus an HTB `max-limit` of 760,000,000 bit/s per direction
per ISP. The original queue types were not edited. A tested 480-second
router-local timer restored each original queue name, rate limit and enabled
state if the controller failed. This compares complete queue configurations,
not the algorithms at a proven identical enforced rate.

| Path and configuration | Completed Mbps | Objects completed | Seconds |
| --- | ---: | ---: | ---: |
| Direct, CAKE before | 980.6 | 128/128 | 33.02 |
| Direct, FQ-CoDel + HTB | 919.5 | 128/128 | 35.22 |
| Direct, CAKE restored | 889.2 | 128/128 | 36.42 |
| RelayWeave, CAKE before | 847.8 | 128/128 | 38.20 |
| RelayWeave, FQ-CoDel + HTB | 768.5 | 128/128 | 42.14 |
| RelayWeave, CAKE restored | 841.4 | 128/128 | 38.49 |

FQ-CoDel did not demonstrate a throughput improvement: its production result
was below both CAKE controls. All six batches completed every object.

This short throughput comparison does not accept interactive latency under
household load or sustained stability. A permanent queue choice requires those
checks as well. FQ-CoDel provides per-flow queuing and delay control; HTB supplies
the rate limit in this candidate. See the
[RouterOS queue documentation](https://manual.mikrotik.com/docs/firewall-and-quality-of-service/queues/).

## Restoration and remaining limits

All temporary changes were rolled back. Queue tree, queue types and simple
queues matched the saved preimage; canonical provider, routing and firewall
fingerprints matched too. Both owned rollback schedulers were removed. All
native test processes exited, temporary listeners closed, and their generated
configuration was removed. The production worker was not restarted or replaced.
After rollback, PC2 checks
passed HTTP CONNECT and SOCKS5 TCP to HTTPS with certificate verification, plus
SOCKS5 UDP DNS. These smoke checks do not resolve the earlier intermittent stalls.

The configured CAKE rates alone are not proof of a hard 1.52 Gbps application
ceiling: the direct ISP1 control exceeded its nominal 760 Mbps setting without a
configuration change. This corrects the earlier interpretation in the
[previous queue report](dualwan-ab-20261025.md). Coverage and enforcement need
separate verification before relying on those values as limits.

The evidence supports reducing shared router processing cost, verifying ISP2
capacity against capable sources, and investigating the slow proxy transfers.
This report did not test adding remote CPU. Forcing the tested proxy pair did
not improve performance. Existing TCP/TLS sessions cannot simply be moved to
another unrelated
proxy without transport support. A generic FastTrack rule is not selected: its
routing and queue bypass behavior must be reconciled with the current ISP pins.

This is a diagnostic report, not a software performance release. It does not
claim a new deployment, a repaired intermittent timeout, 1.5 Gbps, or a sustained
speed acceptance. See [the sustained-throughput plan](sustained-throughput.md).
