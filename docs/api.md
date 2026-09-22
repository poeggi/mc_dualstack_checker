# API

Two layers. The page talks to `api.php` on the web host; `api.php` relays probes to the backend. The backend is public as well and is the API to use from your own code; it allows cross-origin requests from any site.

All responses are JSON with `Cache-Control: no-store`. Errors are `{"error": "<message>"}` with a 4xx/5xx status.

## Site API: `https://www.poggensee.it/mc_dualstack_check/api.php`

| `op` | Parameters | Answer |
|---|---|---|
| `config` | - | `{"provider": "own" \| "mcsrvstat" \| "", "version": "<release>"}` |
| `resolve` | `host` | `{"a": [...], "aaaa": [...], "errors"?: {"a"?: "...", "aaaa"?: "..."}}` |
| `ping` | `ip`, `port`, `edition`, `host`? | the provider's probe result, see below |
| `health` | - | the backend's `/healthz`, or `{}` for other providers |

`resolve` runs on the web host (PHP `dns_get_record`). Empty lists mean no record. An entry in `errors` means the lookup itself failed, which is reported separately so a failed lookup is never shown as a missing record.

`ping` validates `ip` (literal IPv4 or IPv6, brackets allowed), `port` (1-65535) and `edition` (`bedrock`, default, or `java`), then relays. With the `own` provider the answer is the backend's `/ping` document. With `mcsrvstat` it is api.mcsrvstat.us's document, unchanged. `host` is the name the user asked about; pass it so port fallbacks and both families count as one system for the limits.

Status codes: `400` invalid parameter, `429` client over a limit (`Retry-After` in seconds), `502` upstream unreachable, `503` no backend configured or backend busy (`Retry-After`).

## Backend API: `https://mcdscheck-api.poggensee.it`

Public. `https://mcdscheck-api.poggensee.it/` is a landing page with these links.

### `GET /ping?ip=<addr>&port=<n>&edition=bedrock|java[&host=<name>]`

One probe against one address and port: RakNet unconnected ping (UDP) for Bedrock, Server List Ping (TCP) for Java. The address family follows `ip`. `host` is only used in the Java handshake, some proxies route on it, and as the system name for the limits.

```json
{
  "state": "online",
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
| `state` | `online`, `offline`, or `no_route` (the checker host has no connectivity for this address family) |
| `rtt_ms` | round trip of the probe, `online` only |
| `error` | the socket or protocol error, `offline` and `no_route` only |
| `cached`, `age_s` | present when answered from the 60 s cache, with the result's age |
| `info` | server data; `gamemode`, `map`, `server_id` are Bedrock only; formatting codes are stripped from `motd` and `map` |

### `GET /resolve?host=<name>`

Same shape as the site API's `resolve`, resolved on the backend host.

### `GET /healthz`

`{"ok": true, "version": "v1.0.0", "ipv6": true}`. `ipv6` reports whether the host has a global IPv6 address; the page warns when it is false. Public, not rate limited.

## Caching

Probe results are cached for 60 seconds per `edition`, `ip` and `port`, online and offline alike. Concurrent identical probes are coalesced into one. Cached answers carry `cached` and `age_s`.

## Limits

Per client address (direct callers: the peer address; through the site: the address the web host forwards):

- **Distinct systems.** More than 10 different systems within 60 seconds starts a 60 second cooldown; every request then answers `429` with `Retry-After`. A system is the `host` given, or the literal address; both families and all port fallbacks of one check count once.
- **Request budget.** 20 requests, refilled at one per second. Over it: `429` with `Retry-After`.

Global: at most 64 probes in flight. Above that the backend answers `503` with `Retry-After: 5` instead of queueing.
