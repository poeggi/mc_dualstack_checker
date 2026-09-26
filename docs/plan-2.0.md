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
  - A weak answer (D3) loses to any complete one and waits for one at most 500 ms. Alone, it still means online.
- Cost for a RakNet-only server: at most 250 ms. Cost on total failure: unchanged, at most 4 s.
- The probe budget (`pingTimeout`, 6 s), the in-flight slots and the limits do not change.

### D2. The NetherNet probe (`backend/nethernet.go`)

- TCP connect to `ip:port`, then `GET /v1/join` over plain HTTP. `Host` is the typed name, or the IP.
  `User-Agent` is the client's, `libhttpclient/1.0.0.0`.
- Vanilla BDS has no TLS and answers plain HTTP, so plain HTTP comes first. When that attempt connected but brought
  no 2xx (a TLS alert, a close, a reset, or an HTTP error from an HTTPS-only server), the same request is sent once
  over TLS: no certificate checks, ALPN `http/1.1`, SNI the typed name. That serves third-party servers that
  require HTTPS (GeyserNetherNet). Status pages read public data; the certificate proves nothing we need.
- Connect and answer within 3 s per attempt (`javaIOTimeout` applies to both TCP editions).
- Accepted: status 2xx, body at most 16 KiB, a JSON object with `version` or `name`. `protocol` may be a number or a string.
- No redirects. No 2xx over either attempt means "no NetherNet", as it does for the client.
- Field mapping: `name` -> motd, `protocol` -> protocol, `version` -> version, `level` -> map, `players` -> players_online,
  `maxPlayers` -> players_max, `gameType` -> gamemode (0 Survival, 1 Creative, 2 Adventure; other numbers as sent).
  `edition` stays empty; the scheme names the transport.
- The result says that signalling answered and that the game path (WebRTC over UDP) is not tested.

### D3. Weak answers

- A RakNet pong with the magic but no string (BDS-23066, 33 bytes) and a NetherNet 2xx with an empty body are weak answers.
  A 2xx with another body that is no status (a web page) is invalid data, not a weak answer.
  Vanilla BDS sends both when `enable-lan-visibility=false` (measured on 1.26.50.5 to 1.26.60.28).
- A weak answer alone gives `state: online` with `info` holding only `scheme`. The page says the server answered
  but hides its details, and names the likely cause (`enable-lan-visibility=false`). Wording is settled with the mockups.

### D4. API additions (`docs/api.md`)

- Request: `id` (Java connection ID, optional). Sent as `host?_id=<id>` in the handshake. DNS and SRV use the bare host.
- `info.scheme`: `raknet`, `nethernet` or `slp`. Present on every online answer.
- `info.contact`: the Java `contact` string, when sent.
- `errors`: on a failed probe, one entry per scheme tried: `{"nethernet": "...", "raknet": "..."}`.
- `error` and `state` keep their meaning. For Bedrock they follow the RakNet leg, as today.
- The cache key is `edition|ip|port|host|id`. A proxy routes on the host, and a server with `allowed-connection-ids`
  hides its status from probes without the ID. A cached answer must never reach a request that sent another name or ID.
  The cached result carries the scheme.
- `players_online` and `players_max` are left out when the server sends no player counts (Java without `players`, weak answers).

### D5. Java (`backend/java.go`)

Facts from the 26.4 Snapshot 1 code (Mojang ships the jars unobfuscated since 26.1):

- The client splits a typed address at the first `@`: `id@host` gives `_id=id`. A typed `host?k=v` is taken as properties too.
- Properties are encoded with Java's `URLEncoder` (form encoding) and decoded with `URLDecoder`.
- The client's status ping sends the same properties as its login, `_id` included. RTT is Pong minus Ping.
- The server compares `_id` exactly with each entry of `allowed-connection-ids` (split at commas, entries trimmed).
- A wrong or missing ID, and `enable-status=false`, both close the TCP connection right after the handshake, without a packet.
- The server reads the host field with a limit of 1024 characters; 26.3 and older read at most 255.

Decisions:

- Connection ID: `id@host` typed in the page is split at the first `@`; the API gets `id`. The handshake carries
  `host?_id=<id>`, encoded like `URLEncoder`, so it is byte for byte what the client sends.
- The ID has 1 to 64 printable ASCII characters, without spaces, commas and `@`. An ID with those can never match.
  `id` with an edition other than Java answers `400`.
- `_o` is not sent: it adds nothing for a status and breaks forced hosts on today's proxies.
- Ping/Pong after the status: RTT is Pong minus Ping. The Pong is waited for at most 1 s.
  When the server closes first or sends something else, RTT is the whole exchange, as today.
- A close or reset after the TCP connect, before any status byte: reason "connected, no status (status disabled or
  connection ID required)", or "(status disabled or wrong connection ID)" when an ID was sent. Silence stays "no response (timeout)".
- Parser: top-level and nested arrays (first element is the parent), `translate` (uses `fallback`, else empty),
  components without text (`object`, `keybind`), missing `players`, `contact`. Fields of the wrong type are dropped one
  by one instead of failing the status. Numbers sent as strings are accepted. Formatting codes are stripped from the version name.
- The handshake keeps sending the SRV target. Up to 26.3 the client's status ping sends the typed host, from 26.4 the target. Documented, not changed.
- Optional, last: the legacy 0xFE ping when the modern status gets no answer at all.

### D6. Frontend (`frontend/mc_dualstack_check.js`, `.css`)

- More section: row "Scheme" with `RakNet (UDP)`, `NetherNet (TCP signalling, game path not tested)` or `Server List Ping`.
  Row "Contact" for Java when present. Row "Connection ID" when one was typed.
- Weak answers show the D3 text in place of the MOTD.
- Port fallbacks stay as they are. 19133 serves RakNet servers with split ports; NetherNet uses one port for both families.
- Log: one line per scheme tried, with its error. Existing lines keep their wording.
- Input: `id@host` for Java. The host part goes through the existing validation; the ID part is limited to 64 characters.
- `mcText` gets a Bedrock palette: 0-9 and a-f with 9 = #447FFF, g-w material colours, m and n as colours,
  no reset on a colour code, no `x` hex sequence. The edition of the check selects the palette.
- The collapsed card keeps its height. Everything new lives under More or in the log.

### D7. Tests

- `backend/ping_test.go`: the race with fake schemes (preferred slow, preferred failing fast, both answering, weak plus complete);
  `/v1/join` parsing (number and string protocol, empty body, HTML body, oversize body); plain-then-TLS retry against a TLS-only
  test server, one that closes on plain HTTP and one that answers it with 400;
  `_id` encoding; Ping/Pong and early close; array and `translate` MOTDs.
- `test/backend.sh`: a fake NetherNet server on loopback (`FILTER_INTERNAL_TARGETS=false` as today); RakNet-only and NetherNet-only cases;
  the `id` parameter; `errors` and `scheme` fields.
- Run the suite on Windows and in WSL with a sane resolver, as for v1.5.x. Render checks in Edge, WebKit and Firefox.
- Local servers, all in scratch, none in the repo: vanilla Java 26.3 and 26.4 Snapshot 1 (plain, with
  `allowed-connection-ids`, with `status-contact-details`, with `enable-status=false`), and BDS 1.26.50.5 to 1.26.60
  in WSL with `transport=nethernet` and `transport=raknet`, LAN visibility on and off, over IPv4 and IPv6 loopback.
  No server data in the repo.

### D8. Documentation

- `README.md`: the two Bedrock schemes and the race, the IPv6 port note for NetherNet (one port, dual-stack), the "game path not tested" caveat,
  servers with LAN visibility off showing no details, Java connection IDs, the Bedrock palette.
- `docs/api.md`: `id`, `info.scheme`, `info.contact`, `errors`, weak answers, timeouts of the race, the SRV qualifier (D5).
- Code comments carry the why: head start and grace, plain HTTP then TLS, no `_o`, weak answers.
- Release notes for v2.0.0 list the additions and the behaviour changes (parallel schemes, weak pong counts as online).
- `docs/probing-2.0.md` stays as research notes; this file records the decisions. Both are updated when a fact changes.

## Known limits

Nothing open blocks the work. What stays unverified:

- The client's TLS-then-HTTP order and its User-Agent come from third-party code, not from first-party code.
- The game path (WebRTC over UDP) is not probed; a NetherNet answer proves the signalling only.
- Java 26.4 is a snapshot. Recheck the ID format at its first pre-release.

## Order of work

All work is local: commits stay on the local branch `v2-work`, so main stays releasable for fixes in between.
Tests run against local servers. Nothing is pushed before the release go.
Each step updates README and `docs/api.md` for what it adds.

1. Java backend: ID, Ping/Pong, parser, no-status reason, `contact`, cache key. Tests, local vanilla servers.
2. Java frontend: `id@host`, rows "Contact" and "Connection ID", missing player counts. Mockups first, commit after approval.
3. Backend: scheme list and the race, RakNet and SLP as the only schemes, `scheme`. Tests stay green; behaviour otherwise unchanged.
4. Backend: NetherNet probe, weak answers, `errors`. Tests, local BDS in both transports.
5. Bedrock frontend: scheme row, log lines, Bedrock palette, weak answers. Mockups first, commit after approval.
6. Docs pass (README, api.md, comments) and the v2.0.0 release notes.
7. Full local test matrix, then the question "ready for 2.0.0?". Release only on a go that names the version.
