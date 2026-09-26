# Plan for 2.0: NetherNet and Java 26.4

Decided 2026-09-26 on the facts in [probing-2.0.md](probing-2.0.md).
Target: released before Bedrock 26.60 (2026-10-27).

## Goals

- The user types host and port as today. No new controls.
- Bedrock is probed over NetherNet and RakNet. NetherNet is preferred.
- Java accepts a connection ID, tolerates the 26.4 additions and measures RTT like the client.
- The card's More section names the scheme that answered. The log shows every scheme tried.
- The API stays backward compatible. Additions only.
- Every probe goes straight from the backend to the typed address. No relay, no third-party service.
- The backend keeps its table-driven shape: probes are pure functions, schemes are data.

## Decisions

### D1. Schemes and the race

- An edition has an ordered list of schemes. Bedrock: `nethernet` (TCP), `raknet` (UDP). Java: `slp` (TCP).
- One probe of `ip:port` runs the edition's schemes as a race with a head start, like Happy Eyeballs:
  - The first scheme starts at once.
  - The next starts 250 ms later, or as soon as the one before it has failed.
  - A complete answer from the preferred scheme wins, even if a later scheme answered first.
  - When a later scheme answers while the preferred one is still pending, the result waits for the preferred one at most 500 ms.
  - A weak answer (D3) loses to any complete one. Alone, it still means online.
- Cost for a RakNet-only server: at most 250 ms. Cost on total failure: unchanged, at most 4 s.
- The probe budget (`pingTimeout`, 6 s), the in-flight slots and the limits do not change.

### D2. The NetherNet probe (`backend/nethernet.go`)

- TCP connect to `ip:port`, then `GET /v1/join` over plain HTTP. `Host` is the typed name, or the IP.
- When the answer is no HTTP (TLS alert, reset, close), the same request is sent over TLS without certificate
  checks, SNI set to the typed name. Status pages read public data; the certificate proves nothing we need.
- Connect and answer within 3 s (`javaIOTimeout` applies to both TCP editions).
- Accepted: status 2xx, body at most 16 KiB, a JSON object with `version` or `name`. `protocol` may be a number or a string.
- No redirects. Any non-2xx means "no NetherNet", as it does for the client.
- Field mapping: `name` -> motd, `protocol` -> protocol, `version` -> version, `level` -> map, `players` -> players_online,
  `maxPlayers` -> players_max, `gameType` -> gamemode (the number as sent; a name mapping needs a verified source first).
  `edition` stays empty; the scheme names the transport.
- The result says that signalling answered and that the game path (WebRTC over UDP) is not tested.

### D3. Weak answers

- A RakNet pong with the magic but no string (BDS-23066, 33 bytes) and a NetherNet 2xx without JSON are weak answers.
- A weak answer alone gives `state: online` with `info` holding only `scheme`. The page shows "answered without status".

### D4. API additions (`docs/api.md`)

- Request: `id` (Java connection ID, optional). Sent as `host?_id=<id>` in the handshake. DNS and SRV use the bare host.
- `info.scheme`: `raknet`, `nethernet` or `slp`. Present on every online answer.
- `info.contact`: the Java `contact` string, when sent.
- `errors`: on a failed probe, one entry per scheme tried: `{"nethernet": "...", "raknet": "..."}`.
- `error` and `state` keep their meaning. For Bedrock they follow the RakNet leg, as today.
- The cache key stays `edition|ip|port`. The cached result carries the scheme.

### D5. Java (`backend/java.go`)

- Connection ID: `id@host` typed in the page is split there; the API gets `id`. The handshake carries `host?_id=<id>`,
  percent-encoded. The ID is at most 64 characters. `_o` is not sent: it adds nothing for a status and breaks forced hosts on today's proxies.
- Ping/Pong after the status: RTT is Pong minus Ping. When the server closes first, RTT is the whole exchange, as today.
- A TCP connect followed by close or silence, with no status packet: reason "connected, no status (status disabled or connection ID required)".
- Parser: top-level array (first element is the parent), `translate` (uses `fallback`, else empty), `object` (empty), missing `players`, `contact`.
- The handshake keeps sending the SRV target. Up to 26.3 the client's status ping sends the typed host, from 26.4 the target. Documented, not changed.
- Optional, last: the legacy 0xFE ping when the modern status gets no answer at all.

### D6. Frontend (`frontend/mc_dualstack_check.js`, `.css`)

- More section: row "Scheme" with `RakNet (UDP)`, `NetherNet (TCP signalling, game path not tested)` or `Server List Ping`.
  Row "Contact" for Java when present. Row "Connection ID" when one was typed.
- Weak answers show "answered without status" in place of the MOTD.
- Log: one line per scheme tried, with its error. Existing lines keep their wording.
- Input: `id@host` for Java. The host part goes through the existing validation; the ID part is limited to 64 characters.
- `mcText` gets a Bedrock palette: 0-9 and a-f with 9 = #447FFF, g-w material colours, m and n as colours,
  no reset on a colour code, no `x` hex sequence. The edition of the check selects the palette.
- The collapsed card keeps its height. Everything new lives under More or in the log.

### D7. Tests

- `backend/ping_test.go`: the race with fake schemes (preferred slow, preferred failing fast, both answering, weak plus complete);
  `/v1/join` parsing (number and string protocol, empty body, HTML body, oversize body); plain-then-TLS retry against a TLS test server;
  `_id` encoding; Ping/Pong and early close; array and `translate` MOTDs.
- `test/backend.sh`: a fake NetherNet server on loopback (`FILTER_INTERNAL_TARGETS=false` as today); RakNet-only and NetherNet-only cases;
  the `id` parameter; `errors` and `scheme` fields.
- Run the suite on Windows and in WSL with a sane resolver, as for v1.5.x. Render checks in Edge, WebKit and Firefox.
- Live: a NetherNet BDS reachable over IPv4 and IPv6, taken from the live-check secrets. No server data in the repo.

### D8. Documentation

- `README.md`: the two Bedrock schemes and the race, the IPv6 port note for NetherNet (one port, dual-stack), the "game path not tested" caveat,
  Java connection IDs, the Bedrock palette.
- `docs/api.md`: `id`, `info.scheme`, `info.contact`, `errors`, weak answers, timeouts of the race, the SRV qualifier (D5).
- Code comments carry the why: head start and grace, plain HTTP then TLS, no `_o`, weak answers.
- Release notes for v2.0.0 list the additions and the behaviour changes (parallel schemes, weak pong counts as online).
- `docs/probing-2.0.md` stays as research notes; this file records the decisions. Both are updated when a fact changes.

## Open facts to settle while implementing

- The `gameType` numbers of `/v1/join` (D2): find a primary source before mapping to names.
- Plain HTTP on every BDS version with NetherNet, and `/v1/join` over IPv6 on a live BDS.
- How a 26.4 server refuses a status without the right `_id` (close, silence or an error). Adjust the D5 reason text if needed.

## Order of work

1. Backend: scheme list and the race, RakNet and SLP as the only schemes. Tests stay green; behaviour unchanged.
2. Backend: NetherNet probe, weak answers, `scheme` and `errors`. Tests.
3. Backend: Java ID, Ping/Pong, parser, no-status reason, `contact`. Tests.
4. Frontend: scheme and contact rows, log lines, `id@host`, Bedrock palette, weak answers. Render checks.
5. Docs: README, api.md, comments.
6. Full local test matrix, then the question "ready for 2.0.0?". Release only on a go that names the version.
