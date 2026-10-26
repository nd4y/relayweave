# CONNECT route diagnostics

**English** | [Русский](connect-diagnostics.ru.md)

The worker can expose the selected route, profile and ISP in a successful HTTP
CONNECT response. This associates a stalled client transfer with its actual
route without decrypting TLS or changing the tunnel copy path.

Diagnostics are disabled by default. To enable them for a trusted diagnostic
client, add an environment entry to the worker service in your deployment:

```yaml
environment:
  WORKER_CONNECT_DIAGNOSTICS_CIDRS: "192.0.2.10/32,2001:db8::10/128"
```

Replace these documentation addresses with the client's source addresses.
The variable is read once when the worker starts. Remove it or set it to an
empty string and recreate the worker to disable diagnostics. Recreating a
worker interrupts its existing connections, so retain the previous deployment
configuration and image for rollback.

The allowlist uses the actual TCP peer. `Forwarded`, `X-Forwarded-For` and other
request headers cannot grant access. If a reverse proxy terminates the client
connection, its address is the peer: allowing that address exposes diagnostics
to every authenticated client behind it. Use the direct worker listener for
individual client restrictions. Normal listener admission and authentication
still apply. Any invalid CIDR or more than 32 entries disables the entire
diagnostic allowlist.

Successful allowed responses contain:

```text
X-RelayWeave-Route-ID: <selected route ID>
X-RelayWeave-Profile-ID: <selected profile ID>
X-RelayWeave-ISP: <selected egress ID>
```

Only bounded identifiers are returned, with no target address, URL, client
identity or subscription credentials. Invalid or absent identifiers suppress
all diagnostic headers. Responses for other clients retain the original
CONNECT status bytes. SOCKS and ordinary HTTP responses are unchanged.

For a controlled, public benchmark target, `curl --dump-header headers.txt`
captures the proxy's CONNECT response before the origin response. Keep one
header file per parallel transfer. A reused connection may have no new CONNECT
response: associate it with its original connection, rather than inventing a
new route. Failures before a successful CONNECT still require separate worker
diagnostics. Route headers identify the chosen path, not the cause of failure.
Do not publish captures from authenticated/private targets.
