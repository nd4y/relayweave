# Router CPU and proxy-path diagnosis, 2026-10-26

**English** | [Русский](cpu-path-diagnostics-20261026.ru.md)

The 1.5 Gbit/s target remains unachieved. Two separate problems were observed:
router CPU saturation during load and intermittent stalls on a specific
proxy + ISP2 path. Later, ISP1 lost its physical Ethernet link. No provider
connection settings were changed by these experiments.

## CPU evidence and link state

A quieter RouterOS profiler, four five-second `cpu=all as-value` samples,
observed approximately 15.62% of total CPU in networking, 15.47% in firewall,
15.09% in queuing and 10.88% in Ethernet processing. One core averaged about
99% while total CPU averaged about 74%. The corresponding completed-file
goodput was 833.746 Mbit/s, 128/128 objects. This identifies a shared processing
constraint, not an exclusive cause of every slow transfer. An earlier
streaming-console profile introduced appreciable observer overhead.

The router recorded ISP1 `link down` at **08:06:42 Europe/Moscow** (the router
itself uses Europe/Samara). Subsequent reads confirmed `no-link`, an enabled
administrative interface, a stopped DHCP client and an unavailable ISP1 route.
The existing failover script enabled its ISP2 fallback rule. This preceded the
RPS experiment; it was not a provider-setting change by that experiment.

RPS A/B/A used a fresh preimage and a tested, router-local 180-second rollback.
It temporarily enabled receive steering on the LAN trunk and two WAN Ethernet
interfaces. Completed-file goodput was 713.313 -> 770.284 -> 729.161 Mbit/s;
completed objects were 123 -> 124 -> 128 out of 128. This is not accepted as a
throughput improvement: ISP1 was unavailable and failures make the samples
unsuitable for a stable-capacity claim. RPS and queues were restored exactly;
the owned rollback scheduler was removed. Provider and routing export hashes
matched the RPS preimage. A combined firewall hash did not match; rule reads
and logs identified the existing failover change relative to the earlier
snapshot, but did not fully explain that hash discrepancy. No firewall rollback
was attempted, avoiding interference with autonomous failover.

Generic FastTrack was not enabled: MikroTik documents its main-table-only
routing, incompatible with blindly bypassing source-selected policy tables.
RPS is also not guaranteed to help hardware that already has several RX
queues. See [MikroTik Resource](https://help.mikrotik.com/docs/spaces/ROS/pages/40992875/Resource)
and [Packet Flow](https://help.mikrotik.com/docs/spaces/ROS/pages/328227/Packet%2BFlow%2Bin%2BRouterOS).

## Path attribution

PC2 downloaded public benchmark objects with verified TLS, no retries,
bounded parallelism, deadlines and byte caps. Native Xray controls copied
current profile settings privately on the deployment host, used explicit
source-IP binding, and admitted only PC2 and specified benchmark hosts.

| Test | Result |
| --- | --- |
| M1/M2: five proxies through each ISP plus two direct controls | 144/144 completed before the observed link outage |
| D1: automatic RelayWeave, 128 large objects, 32 parallel | 128/128, 608.041 Mbit/s; all transfers attributed to ISP2 |
| D2: same workload, later automatic choices | 123/128, 457.144 Mbit/s; all five 60-second partial-transfer timeouts on path P4 + ISP2 |
| M3: five identical proxies tested through both ISPs after link loss | All 20 ISP1 proxy transfers and four direct controls timed out; all 24 ISP2 transfers completed |
| M4: fixed P4 + ISP2 versus two other proxies on ISP2, eight large objects each | P4 completed 7/8 with one 60-second partial-transfer timeout; controls completed 16/16 |

D2 assigned 11 transfers to P4 and five timed out; its other 117 transfers
completed. M4 reproduced the symptom without RelayWeave's route selector.
Thus the symptom follows this path under the tested conditions; it does not
prove whether the remote proxy, its upstream, the destination or their
interaction is responsible. Server addresses and profile credentials are
omitted here. Small-object success did not establish sustained path quality.

## Shipped change and acceptance

[Opt-in CONNECT diagnostics](connect-diagnostics.md) expose selected route,
profile and egress identifiers only to explicitly allowed actual TCP peers.
No traffic decryption or tunnel-copy changes were introduced. They identify
the selected path, not the cause of a timeout.

The worker was built on build-01 with `go test -race ./...`, `go vet ./...`,
wire-level CONNECT/authentication/spoof tests, and real-core protocol/rollover
tests on Xray 26.10.17 and 26.9.30. Deployed image:
`sha256:edcb325987be09100c68e2aab3c63304d73c84da4f7e33c9ecc13ef41fd0520b`.
Only the worker container and its diagnostic allowlist were changed through
Portainer, with protected compose/Env backups and the previous image retained.
No data migration was performed.

PC2 acceptance passed HTTP CONNECT and SOCKS5 TCP with HTTPS 204 and valid TLS,
plus SOCKS5 UDP DNS. A different client received no diagnostic headers even
with a forged `X-Forwarded-For`. Both native fixtures were stopped, their
processes reaped, ports closed and private generated configurations removed.
The automatic worker had no ready ISP1 routes after link loss.

The next capacity comparison requires restoration of the physical ISP1 link.
The intermittent P4 path needs sustained-quality admission/cooldown evidence;
this diagnostic release does not implement a new quarantine policy, and a
brief successful probe must not be reported as resolving its transfer stalls.
