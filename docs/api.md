# API

The API is the backend. A live instance runs at `https://mcdscheck-api.poggensee.it`. Use it from your own code; it allows cross-origin requests from any site. Its root is a landing page. It links the web interface, this document and the source.

NOTE: The live instance at poggensee.it is free to use for non-commercial users or purposes only.

The web interface sends its probes to the API directly from the browser. It reads the API's health through its web host (see README).

All answers of `/ping` and `/health` are JSON with `Cache-Control: no-store`. Errors are `{"error": "<message>"}` with a 4xx/5xx status. Cross-origin pages can read `Retry-After`. `/ping` and `/health` answer methods other than GET and HEAD with `405`. Other paths answer `404`. The live instance also serves its landing page at `/`.

## `GET /ping`

```
GET /ping?ip=<addr>&port=<n>&edition=bedrock|java[&host=<name>]
GET /ping?host=<name>&family=4|6&port=<n>&edition=bedrock|java
```

One probe against one address and port: RakNet unconnected ping (UDP) for Bedrock, Server List Ping (TCP) for Java. `edition` defaults to `bedrock`.

With `ip` (literal IPv4 or IPv6, brackets allowed), the address family follows `ip`. An IPv4-mapped IPv6 address (`::ffff:a.b.c.d`) answers `400`; give the IPv4 address instead. Without `ip`, the backend resolves `host` and probes its first public address in `family`: `4` for the A record, `6` for AAAA. The two families never fall back to each other.

`host` is also sent in the Java handshake, since some proxies route on it. It is the system name for the limits.

`host` must be a DNS name of letters, digits and hyphens, labels of at most 63 characters, 253 in total; a trailing dot is allowed. Otherwise the request answers `400`, or with `ip` the name is ignored.

Internal addresses in `ip` answer `400`: loopback, unspecified, link-local, RFC 1918, shared (`100.64.0.0/10`), unique local (`fc00::/7`), site-local, multicast and reserved ranges. NAT64 forms of those count as well.

Name lookups reveal nothing internal. These answer `no_dns`, exactly like a name without a record:

- Names that are never looked up: single labels, `localhost`, and names under `.localhost`, `.local`, `.internal`, `.lan`, `.home`, `.corp`, `.localdomain`, `.intranet`, `.private`, `.arpa`, `.test`, `.example`, `.invalid` or the checker host's own DNS search domains.
- Names whose records in `family` are all internal addresses.

Names are looked up as absolute names, so the checker host's search domains are never appended.

```json
{
  "state": "online",
  "ip": "2001:db8::1",
  "rtt_ms": 23,
  "cached": true,
  "age_s": 41,
  "info": {
    "edition": "MCPE",
    "motd": "My Bedrock Server",
    "version": "1.21.50",
    "protocol": "766",
    "players_online": 3,
    "players_max": 20,
    "gamemode": "Survival",
    "map": "Bedrock level",
    "server_id": "1234567890123456789"
  }
}
```

| Field | Meaning |
|---|---|
| `state` | `online`, `offline`, `unreachable`, `no_route`, `no_dns` or `dns_error`, see below |
| `ip` | the probed address |
| `rtt_ms` | round trip of the probe, `online` only |
| `error` | short reason, all states but `online` and `no_dns`, see below |
| `cached`, `age_s` | `cached` is present when answered from the 60 s cache; `age_s` is the result's age in seconds, 0 for a fresh probe |
| `info` | server data; `gamemode`, `map`, `server_id` are Bedrock only; formatting codes are stripped from `motd` and `map` |

States:

- `online`: the server answered.
- `offline`: no valid answer. The server was silent, refused the port, or sent data that is not a Minecraft status.
- `unreachable`: something other than the server rejected the probe: a router or firewall on the way answered with ICMP unreachable or administratively prohibited.
- `no_route`: the checker host itself has no connectivity for this address family.
- `no_dns`: `host` has no record in `family`. Name lookups only.
- `dns_error`: resolving `host` failed. Name lookups only.

`error` per state:

- `offline`: `no response (timeout)`, `refused (port closed)`, `refused (reset)`, `refused (closed)`, `invalid data (<detail>)` such as `invalid data (bad raknet magic)`, or `failed (unknown error)`.
- `unreachable`: `rejected (no route to host)`, `rejected (host unknown)`, `rejected (prohibited)` or `rejected (network unreachable)`.
- `no_route`: `network unreachable`, `no source address` or `family not supported`.
- `dns_error`: `timeout` or `error`.

## `GET /health`

`{"ok": true, "version": "v1.1.0", "ipv6": true}`.

The backend refreshes this status every 7 seconds. A request returns the last result. `ipv6` reports whether the host has a global IPv6 address; the page warns when it is false.

## Caching

Probe results are cached for 60 seconds per `edition`, address and `port`, online and offline alike. Concurrent identical probes are coalesced into one. Cached answers carry `cached: true`; `age_s` is always present, 0 for a fresh probe.

## Limits

Per client address: an IPv4 address, or the /64 of an IPv6 address. Callers are charged for their own address, so visitors of the web interface for theirs. When the web interface's host passes a request on, it forwards the visitor's address.

- **Distinct systems.** More than 10 different systems within 60 seconds start a 60 second cooldown. A system is the `host` given, or the literal address. Both families and all port fallbacks of one check count once.
- **Health.** More than 4 `/health` requests within 7 seconds start the same cooldown.
- **Request budget.** 20 requests, refilled at one per second. Over it: `429` with `Retry-After`.

During a cooldown every valid request answers `429` with `Retry-After`.

Global: at most 128 probes and name lookups in flight. Above that the backend answers `503` with `Retry-After: 5` instead of queueing.

At most 10000 clients and 10000 cached results are kept. Beyond that, new clients share one set of limits. Probes then run uncached.

## Running your own

The backend listens on `127.0.0.1` only; put a reverse proxy in front of it. It reads `PORT` (default `8080`), `ALLOWED_ORIGINS` (comma-separated, `*` for any) and `FILTER_INTERNAL_TARGETS`. The last one defaults to `true`; `false` allows probes to internal addresses and internal answers of name lookups, for tests against local servers. Local names such as `.lan` are still never looked up; use the server's IP address.
