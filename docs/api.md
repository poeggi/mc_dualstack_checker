# API

The public API is the backend at `https://mcdscheck-api.poggensee.it`. Use it from your own code; it allows cross-origin requests from any site. `https://mcdscheck-api.poggensee.it/` is a landing page with these links.

The web interface reaches the backend through its frontend, a relay on the web host (see README). That relay is part of the page, not a public API.

All responses are JSON with `Cache-Control: no-store`. Errors are `{"error": "<message>"}` with a 4xx/5xx status. `/ping` and `/health` answer methods other than GET and HEAD with `405`. Other paths answer `404`.

## `GET /ping`

```
GET /ping?ip=<addr>&port=<n>&edition=bedrock|java[&host=<name>]
GET /ping?host=<name>&family=4|6&port=<n>&edition=bedrock|java
```

One probe against one address and port: RakNet unconnected ping (UDP) for Bedrock, Server List Ping (TCP) for Java. `edition` defaults to `bedrock`.

With `ip` (literal IPv4 or IPv6, brackets allowed), the address family follows `ip`. Without `ip`, the backend resolves `host` and probes its first address in `family`: `4` for the A record, `6` for AAAA. The two families never fall back to each other.

`host` is also sent in the Java handshake, since some proxies route on it. It is the system name for the limits.

`host` must be a DNS name of letters, digits and hyphens, labels of at most 63 characters, 253 in total; a trailing dot is allowed. Otherwise the request answers `400`, or with `ip` the name is ignored.

Internal addresses in `ip` answer `400`: loopback, unspecified, link-local, RFC 1918, shared (`100.64.0.0/10`), unique local (`fc00::/7`), site-local, multicast and reserved ranges. IPv4-mapped and NAT64 forms of those count as well.

Name lookups reveal nothing internal. These answer `no_dns`, exactly like a name without a record:

- Names that are never looked up: single labels, `localhost`, and names under `.local`, `.internal`, `.lan`, `.home`, `.corp`, `.localdomain`, `.intranet`, `.private`, `.arpa`, `.test`, `.example`, `.invalid` or the checker host's own DNS search domains.
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
    "version": "1.26.51",
    "protocol": "2193",
    "players_online": 2,
    "players_max": 12,
    "gamemode": "Survival",
    "map": "Bedrock level",
    "server_id": "1234567890123456789"
  }
}
```

| Field | Meaning |
|---|---|
| `state` | `online`, `offline`, `no_route`, `no_dns` or `dns_error`, see below |
| `ip` | the probed address |
| `rtt_ms` | round trip of the probe, `online` only |
| `error` | short reason, `offline`, `no_route` and `dns_error` only: `timeout`, `connection refused (port closed)`, `connection reset`, `network unreachable`, `invalid reply: ...`, `lookup failed` and similar |
| `cached`, `age_s` | `cached` is present when answered from the 60 s cache; `age_s` is the result's age in seconds, 0 for a fresh probe |
| `info` | server data; `gamemode`, `map`, `server_id` are Bedrock only; formatting codes are stripped from `motd` and `map` |

States:

- `online`: the server answered.
- `offline`: no answer. A router or firewall rejecting the probe is `offline` too.
- `no_route`: the checker host itself has no connectivity for this address family.
- `no_dns`: `host` has no record in `family`. Name lookups only.
- `dns_error`: resolving `host` failed. Name lookups only.

## `GET /health`

`{"ok": true, "version": "v1.1.0", "ipv6": true}`.

The backend refreshes this status every 7 seconds. A request returns the last result. `ipv6` reports whether the host has a global IPv6 address; the page warns when it is false.

## Caching

Probe results are cached for 60 seconds per `edition`, address and `port`, online and offline alike. Concurrent identical probes are coalesced into one. Cached answers carry `cached: true`; `age_s` is always present, 0 for a fresh probe.

## Limits

Per client address: an IPv4 address, or the /64 of an IPv6 address. Direct callers are charged for their own address, the web interface for the address its host forwards.

- **Distinct systems.** More than 10 different systems within 60 seconds start a 60 second cooldown. A system is the `host` given, or the literal address. Both families and all port fallbacks of one check count once.
- **Health.** More than 4 `/health` requests within 7 seconds start the same cooldown.
- **Request budget.** 20 requests, refilled at one per second. Over it: `429` with `Retry-After`.

During a cooldown every request answers `429` with `Retry-After`.

Global: at most 128 probes in flight. Above that the backend answers `503` with `Retry-After: 5` instead of queueing.

## Running your own

The backend listens on `127.0.0.1` only; put a reverse proxy in front of it. It reads `PORT` (default `8080`), `ALLOWED_ORIGINS` (comma-separated, `*` for any) and `FILTER_INTERNAL_TARGETS`. The last one defaults to `true`; `false` allows probes to internal addresses and internal answers of name lookups, for tests against local servers.
