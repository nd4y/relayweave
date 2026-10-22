# Proxy + ISP paths

RelayWeave can compile each eligible proxy profile into independently probed
`(profile, egress)` paths. Enable this in Settings / ISP channels, then plan and
apply a configuration. Existing deployments without `egress_paths` retain their
previous routing. No router, host address or WAN interface is changed by the UI.

```json
"egress_paths": [
  {"id":"isp1","name":"Primary","source_ip":"192.0.2.11","enabled":false,"weight":8,"download_mbps":760,"upload_mbps":760},
  {"id":"isp2","name":"Secondary","source_ip":"192.0.2.12","enabled":true,"weight":5,"download_mbps":475,"upload_mbps":475}
]
```

These example source addresses are documentation literals. Replace them with
reserved addresses actually assigned in the worker's network namespace. At least
one configured path must be enabled. Up to four paths and 10,000 compiled routes
are supported. Balanced policies must have enough active path slots for enabled
ISPs; the existing 16 hot path / 32 process limits remain. No per-ISP worker is
required. Settings are global and currently assume one worker or identical local
egress addressing across worker network namespaces.

## What sendThrough does

`sendThrough` is Xray's outbound source IP option, not an interface, gateway,
provider identifier or bonding protocol. The normal system dialer binds the TCP
or UDP socket to that address. The address must belong to the worker namespace
and match the remote address family. Source selection by itself does not enforce
the physical ISP. DNS resolution of proxy hostnames may still use the system
resolver; a source binding is not a claim that all resolver traffic uses that ISP.

| Environment | Applicability |
| --- | --- |
| Native Linux, including Synology Linux | Source IP binding plus strict OS/router policy routing. Production target. |
| Linux Docker host network | Host addresses are available to Xray. Recommended for the current deployment. |
| Linux Docker bridge | Only addresses inside that namespace can be bound. Host aliases are insufficient; NAT may erase the source distinction before the router. Requires deliberate routing/SNAT configuration. |
| Native Windows and macOS | Xray's common TCP/UDP source binding applies; local address availability and OS/router routing still decide the path. Not certified by this Linux test suite. |
| Native FreeBSD/OpenBSD | Same source-binding design in the system dialer, subject to OS routing. No native acceptance run here. |
| Docker Desktop on Windows/macOS | Linux VM networking differs from the host; Desktop host networking has specific-IP binding limitations. Do not assume Windows/macOS LAN aliases are available to the container. |
| WSL2 | Uses the WSL network namespace. NAT and mirrored networking need separate verification; Windows host addresses are not automatically usable source addresses. |
| Android/iOS embedded cores | App VPN/protected sockets and custom dialers can override system behavior. No blanket support claim. |

This implementation targets Xray's normal system dialer and supported proxy
transports. It overrides a profile's source binding with the operator's selected
path. Profiles specifying another dialer, interface, nonzero socket mark or proxy
detour are rejected for multi-ISP compilation instead of silently bypassing the
path. The test matrix includes approved Xray 26.10.17 and 26.9.30, TCP/UDP actual
peer source checks, unassigned-source failure, HTTP/SOCKS TCP/UDP and lane disable.
Other core families are extension points, not validated multi-ISP implementations.

`sockopt.mark` is a Linux socket mark local to the kernel; it is not carried to a
separate MikroTik router. `sockopt.interface` and `sendThrough` are different
options. Assigning an address or modifying routing needs host administrator
privileges; using an already assigned normal source IP does not require adding
NET_ADMIN to the worker. RelayWeave itself remains a Linux service because other
worker components use Linux-specific APIs; portable Xray is not a portable
RelayWeave runtime claim.

## Routing contract

Reserve two source IPs outside DHCP. Give each one a strict source-policy route
through its intended WAN and appropriate return NAT. Private/control traffic must
retain local routes. Check existing mangle precedence, VPN policies, FastTrack,
reverse-path filtering, MTU/MSS and per-WAN shaping. Enforce a blackhole/no-fallback
route when a selected WAN is unavailable. Otherwise a healthy probe can silently
run through the wrong ISP. Prefer dedicated routing objects over changing all NAS
traffic or reusing another application's source ports.

Persist source aliases before worker startup and maintain dynamic WAN gateways.
When a WAN is disabled, leave its application path disabled too. Enabling the UI
path never enables a physical interface. A physical disconnect test requires a
separate maintenance window or an isolated simulated failure.

## Scheduling and observability

In auto/throughput policies, ISP and shared-IP profile weights are calculated
automatically from quality, throughput observations, capacity priors and active
load. Explicit round-robin retains configured ISP weights. Optional read-only
router telemetry can reduce new-flow allocation toward a congested WAN or avoid
a freshly reported down link. Missing/stale telemetry leaves autonomous routing
active. See [router integrations](router-integrations.md) for semantics, smoothing
and fallback. Subscription aliases never multiply shared endpoint capacity.
These are new-connection allocation estimates, not byte fairness or a guaranteed
measurement of spare Internet capacity. Each route's speed ceiling is capped by its ISP download budget;
upload settings are descriptive capacity budgets, not an upload shaper.

Each healthy ISP gets a representative in the prepared candidate window. Server
IP overlap grouping is retained; different ISP variants share the original remote
balancer identity, rather than claiming extra server capacity. Probes, circuit
breakers and delivery observations stay per path. Disabled paths are not compiled
or probed; retiring existing sessions may drain. Retry order includes other ISPs.

Fallback preserves the first healthy logical profile and its profile retry order,
but allocates new connections between that profile's healthy ISP variants using
automatic lane weights. Pinning a profile therefore does not pin an ISP, even
when the profile window is one. Disabled or freshly reported down lanes are
excluded; existing connections keep their path.

Manual/timed/sticky/latency modes retain their existing ordering semantics. Manual
means the first compiled path. A TCP connection or UDP association stays on one
path; existing TCP sessions cannot migrate to another public source IP. This is
parallel-flow balancing, not single-stream bonding.

Worker status includes `egress_paths`, per-engine `egress`, `profile_id`, `route_id`.
Topology routes and decisions include `egress`, and expanded cards show ISP plus
original profile ID. The decision method `isp_weighted_path_choice` identifies
lane scheduling. Prometheus `proxy_route_egress_info` joins route/engine series to
ISP/profile; `proxy_isp_enabled`, `proxy_isp_weight` and
`proxy_isp_download_limit_bytes_per_second` expose configured lane state. These
labels describe intended enforced policy, not independent packet-capture proof.

## Acceptance and recovery

Run Go race/vet, actual approved-core TCP/UDP tests and browser tests on build-01.
Observe actual outer source IP at the receiver/router. A remote proxy's public
exit IP cannot prove which local ISP was used. Compare single-lane and dual-lane
parallel workloads only when both physical WANs are active; do not claim summed
throughput from loopback fixtures. Check HTTP CONNECT, SOCKS TCP, SOCKS UDP and
authenticated consumers after deployment.

Before deployment, checkpoint encrypted application state, database and router
configuration, and record container/image identities. Keep old images available.
For rollback, restore the application configuration while leaving source aliases
and strict routes in place until old sessions drain. Do not flush conntrack or
delete routes carrying active sessions. An image rollback may require restoring
the previous worker journal/config schema; use the protected checkpoint rather
than feeding new path fields into an older binary.

References: [Xray outbound](https://xtls.github.io/en/config/outbound.html),
[system dialer implementation](https://github.com/XTLS/Xray-core/blob/main/transport/internet/system_dialer.go),
[supported platforms](https://xtls.github.io/en/document/install.html),
[Docker host networking](https://docs.docker.com/engine/network/drivers/host/),
[RouterOS policy routing](https://help.mikrotik.com/docs/spaces/ROS/pages/59965508/Policy%2BRouting).
