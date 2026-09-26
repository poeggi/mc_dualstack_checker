# Probing 2.0: research notes

Input for the 2.0 design. Collected 2026-09-26 by web research and
checked against primary sources the same day. Claims marked "unverified"
rest on secondary sources or single observations. The decisions are in
[plan-2.0.md](plan-2.0.md).

## Goal

- Support the current and the upcoming probe schemes of both editions.
- The user types host and port as today. The right scheme is found automatically.
- The card's More section names the scheme that answered.
- The log shows every scheme tried.

## How we probe today (v1.5.x)

Java (`backend/java.go`):

- TCP. Handshake with protocol -1, the typed host (or the SRV target) and the port. Intent 1 (status).
- Status request, then one status response (JSON). The connection closes after it.
- No Ping/Pong (0x01). RTT is the whole exchange.
- No legacy 0xFE ping.
- Description: a string, or an object with `text`, `extra` and style fields.
  Not handled: a top-level array, `translate`, components without `text`.

Bedrock (`backend/bedrock.go`):

- UDP. RakNet Unconnected Ping (0x01), two tries of 2 s.
- Unconnected Pong (0x1c) with the string `MCPE;motd;protocol;version;players;max;id;level;mode;modeNum;port4;port6;`.
- A pong without that string is "invalid data".
- Frontend port order for IPv6: the typed port (default: the IPv4 port, 19132), then 19133, then 19132.
  Reason for the 19133 fallback: BDS cannot bind IPv4 and IPv6 to one port over RakNet.

Frontend (`frontend/mc_dualstack_check.js`):

- `mcText` renders section-sign codes with Java meanings for both editions.

## Bedrock

### B1. NetherNet replaces RakNet (highest impact)

- The `transport` property exists since BDS 1.26.30 (default `raknet` there).
- BDS 1.26.50 (2026-09-15) made `nethernet` the default: https://minecraft.wiki/w/Bedrock_Edition_Preview_26.50.24
  The shipped server.properties of 1.26.50.5, 1.26.51.1 and 1.26.52.3 carry `transport=nethernet`.
  A 1.26.5x server that still answers RakNet runs with an explicit `transport=raknet`.
  It works, but logs "TRANSPORT TYPE ERROR ... NetherNet is the only supported transport type": https://mojira.dev/BDS-23108
- 26.60 (scheduled 2026-10-27) turns the RakNet warning into an error: https://minecraft.wiki/w/Bedrock_Edition_26.60
  WaterdogPE's docs say RakNet leaves client and BDS in 26.60; an unmerged Geyser docs PR says the same.
  Mojang's own notes say only "RakNet is deprecated". Removal is unverified:
  https://docs.waterdog.dev/waterdogpe-setup/nethernet-configuration
- In NetherNet mode BDS opens no UDP socket. A RakNet ping gets no answer.
  BDS-23111 shows the unanswered pings (LAN discovery). Status sites show such servers as offline:
  https://github.com/mcstatus-io/mcutil/issues/15
- Clients since 26.40 probe NetherNet and send RakNet pings at the same time (BDS-23111 captures).
- NetherNet BDS listens on:
  - TCP `server-port`, dual-stack (`[::]:19132`). With no dual-stack socket available it falls back to IPv4 only (bedrock_server_how_to.html).
  - UDP 7551 for LAN discovery and ICE negotiation. The packets are encrypted with a fixed, public key.
  - ephemeral UDP ports, or `server-udp-ports`, for WebRTC media.

### B2. The NetherNet status endpoint

- Mojang doc, last commit 2026-09-10:
  https://github.com/Mojang/bedrock-protocol-docs/blob/main/additional_docs/NetherNetOnboardingGuide.md
- `GET /v1/join` on TCP `server-port`. No auth.
  Answers 2xx and JSON: `name, protocol, version, level, players, maxPlayers, gameType`.
  Any non-2xx means "no NetherNet".
- It has no port4/port6 and no server ID.
- BDS answers plain HTTP and TLS on the same port (a health check over plain HTTP passes while
  a client's TLS handshake runs: https://github.com/itzg/docker-minecraft-bedrock-server/issues/680).
  mc-monitor in "auto" mode sends `GET http://host:port/v1/join` with a 3 s timeout, then falls back to RakNet:
  https://github.com/itzg/mc-monitor/pull/172
- A 1.26.51 client opened with a TLS ClientHello on 19132 and did not fall back to plain HTTP against
  a server without TLS: https://github.com/Pumpkin-MC/Pumpkin/issues/3738
  A third-party comment says "HTTPS then HTTP". Mojang's guide states no order. Unverified.
  Over plain HTTP the client pins the operator key on first use (TOFU prompt).
- Answers vary: `protocol` comes as a number or a string (mc-monitor accepts both);
  a relay answered 2xx with an empty text/plain body: https://github.com/GeyserMC/GeyserNetherNet/issues/3
- A 2xx proves only the TCP signalling path. Game traffic is WebRTC over UDP on other ports.
  "Visible but cannot join" is an open bug: https://mojira.dev/BDS-23108
  Testing the media path needs an SDP offer whose `a=identity` carries a GameServerToken JWT;
  a server may accept offers without one (server policy).
- Geyser supports external signalling hosts (Warden, `*.wdn.gg`). Its default transport is still RakNet.
  The signalling address can differ from the game host.

### B3. The IPv4/IPv6 port split

- server.properties docs: `server-portv6` "is ignored when transport=nethernet and a dual-stack socket will be opened on server-port instead":
  https://minecraft.wiki/w/Server.properties
- So with NetherNet, IPv6 uses the IPv4 port. The 19133 fallback holds for RakNet only.
- One BDS-23108 comment reports NetherNet over IPv6 working only once. Single observation.

### B4. Pong quirks (RakNet)

- BDS-23066 (open, 1.26.30.5 to 1.26.32.2): with `enable-lan-visibility=false`, the pong ends after the magic.
  It is 33 bytes, with no string: https://mojira.dev/BDS-23066
  Today we report that as invalid data. It should count as online with no MOTD.
- gophertunnel and Dragonfly append `0;0;` after port6. Extra fields must be ignored.
- No format change announced.
- Cloudburst RakNet (Geyser, WaterdogPE) checks only the magic.
  It answers 0x01 and ignores 0x02. It needs no padding.
  Its per-IP limiter resets every 10 ms; an address over the limit is blocked for 10 s. Two tries 2 s apart are fine.

### B5. Bedrock formatting codes

- Current list and hex values: https://minecraft.wiki/w/Formatting_codes
  The Microsoft page (learn.microsoft.com, rawmessagejson) dates from 2023 and lacks v and w.
- m and n are colours in Bedrock. Bedrock has no strikethrough or underline.
- k, l, o and r work as in Java.
- A colour code does not reset bold or italic in Bedrock. In Java it does.
- No hex colours (no `x` sequence).
- 26.50 changed code 9 from #5555FF to #447FFF and retuned the material colours.

| Code | Hex | Name | Added |
|---|---|---|---|
| g | #EFCE16 | minecoin gold | long-standing |
| h | #D9CCB8 | quartz | 1.19.80 |
| i | #A9B4B7 | iron | 1.19.80 |
| j | #8F727D | netherite | 1.19.80 |
| m | #EE222C | redstone | 1.19.80 |
| n | #C87363 | copper | 1.19.80 |
| p | #FFBF1E | gold | 1.19.80 |
| q | #13A045 | emerald | 1.19.80 |
| s | #5FECFF | diamond | 1.19.80 |
| t | #577BFF | lapis | 1.19.80 |
| u | #B66CDD | amethyst | 1.19.80 |
| v | #FF6A00 | resin | 1.21.50 |
| w | #8BB3FF | party blue | 26.30 |

### B6. Server list and Discovery

- A redesigned Servers tab is in the 26.60 previews. UI only, as far as known.
- No other change to how clients ping third-party servers is known.

## Java

### J1. Connection IDs and handshake properties (snapshot 26.4)

- 26.4 Snapshot 1 (2026-09-22): https://minecraft.wiki/w/Java_Edition_26.4_Snapshot_1
  Only in the snapshot so far. It may change before release.
- The handshake host field may carry `host?key=value&key2`, percent-encoded. Keys starting with `_` are reserved.
- A typed `<id>@<host>` sends `host?_id=<id>`.
- With SRV the client sends `target?_o=<original>:<port>`.
- The field maximum grows to 1024.
- New server property `allowed-connection-ids`, "works both for status and login connections".
  A probe without the right `_id` gets no status. How it refuses is not documented.
- New `status-contact-details`: adds `contact` to the status JSON.
- New `enable-legacy-status`: can switch off the 0xFE ping.

### J2. The status description

- Status is still a JSON string. 26.3 is protocol 777, max 32767 characters:
  https://minecraft.wiki/w/Java_Edition_protocol/Packets
  The 1.20.3 NBT chat change applies to play packets and the configuration Disconnect; status and login Disconnect stay JSON.
- Vanilla 1.20.3+ and Adventure emit unstyled parts as plain strings. `extra` elements can be strings.
  We handle that.
- A translate-only MOTD was seen in the wild (mcstatus issue 319, 2022).
- Components without text (`object`) exist since 1.21.9.
- A top-level array is legal: the first element is the parent, the rest its extra. No mainstream server is known to send one.
- 26.1-pre2 (2026-03-13): the client cuts MOTD nesting deeper than 16 levels.
  https://minecraft.wiki/w/Text_component_format
- snake_case events (1.21.5) and `shadow_color` (1.21.4) do not change the visible text.

### J3. Ping/Pong and proxies

- No server is known to require the Ping (0x01).
- BungeeCord's connection throttle (default 3 per 4 s per IP) counts every connection.
  It lifts only after Ping/Pong, so probes that close early count against it. Off with PROXY protocol.
- Velocity with ping-passthrough holds the status until its backend pings finish.
- TCPShield serves a cached MOTD.
- Vanilla times the RTT from the Ping to the Pong.
- When the modern status fails, vanilla falls back to the legacy 0xFE ping.

### J4. Protocol -1

- The convention per the wiki, which warns that some servers may close on an invalid version.
- Stock Velocity shows its newest version. BungeeCord shows its own. Vanilla ignores the field.
  Large networks (2b2t, Pika) echo -1.
- mcstatus and minecraft-server-util send 47; mcutil sends -1. Velocity drops hex colours for 47.
- go-mc sends its real version.

### J5. SRV and the client

- No change: `_minecraft._tcp`, looked up whenever the port is 25565, typed or default.
- The login sends the SRV target. The status ping up to 26.3 sends the typed host and port
  (MC-278651, fixed in 26.4 Snapshot 1, which sends the target plus `_o`).
- TCPShield shows "Invalid Hostname" when SRV points at its CNAME.
- No Happy Eyeballs in the client: MC-255735, still open (2026-04-30). The client tries only the first resolved address.

### J6. Fields and values to tolerate

- `enforcesSecureChat`, `previewsChat` (gone since 1.19.3), `preventsChatReports` (mod), `forgeData`, `modinfo`, `contact`.
- A missing `players` object.
- A snapshot protocol (26.4 Snapshot 1 is 1073742163), or a fake one from maintenance plugins.
- Section-sign codes in `version.name`.
- `enable-status=false`: vanilla sends no status at all.

## Not verified

- Which order the client uses for HTTPS and plain HTTP (B2).
- Whether a trailing dot appears in the SRV target the client sends.
- That no pong format change and no other client ping change is coming (B4, B6).
- Fake protocol numbers from maintenance plugins and colour codes in `version.name` (J6).
