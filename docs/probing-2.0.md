# Probing 2.0: research notes

Input for the 2.0 design. Collected 2026-09-26 by web research.
Claims marked "unverified" come from secondary sources only.
Nothing here is decided yet.

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
- Frontend port order for IPv6: 19133, then 19132.
  Reason: BDS cannot bind IPv4 and IPv6 to one port over RakNet.

Frontend (`frontend/mc_dualstack_check.js`):

- `mcText` renders section-sign codes with Java meanings for both editions.

## Bedrock

### B1. NetherNet replaces RakNet (highest impact)

- BDS 1.26.50 (2026-09-15) added `transport=nethernet`.
  Some sources say it is the default: https://minecraft.wiki/w/Bedrock_Edition_Preview_26.50.24
  Some 1.26.5x servers still answered RakNet pings on 2026-09-26. So the default is not universal. Unverified.
- 26.60 (scheduled 2026-10-27) turns the RakNet warning into an error: https://minecraft.wiki/w/Bedrock_Edition_26.60
  Geyser and WaterdogPE docs say RakNet leaves client and BDS in 26.60. Unverified:
  https://docs.waterdog.dev/waterdogpe-setup/nethernet-configuration
- In NetherNet mode BDS opens no RakNet socket. A RakNet ping then gets no answer.
  Status sites already show such servers as offline:
  https://mojira.dev/BDS-23111 and https://github.com/mcstatus-io/mcutil/issues/15
- NetherNet BDS listens on:
  - TCP `server-port`, dual-stack (`[::]:19132`),
  - UDP 7551 (encrypted LAN broadcast only),
  - ephemeral UDP ports, or `server-udp-ports`, for WebRTC media.

### B2. The NetherNet status endpoint

- Mojang doc, updated 2026-09-10:
  https://github.com/Mojang/bedrock-protocol-docs/blob/main/additional_docs/NetherNetOnboardingGuide.md
- `GET /v1/join` on TCP `server-port`. No auth.
  Answers 2xx and JSON: `name, protocol, version, level, players, maxPlayers, gameType`.
- It has no port4/port6 and no server ID.
- BDS answers plain HTTP. The itzg image health check uses `curl http://127.0.0.1:$PORT/v1/join`:
  https://github.com/itzg/docker-minecraft-bedrock-server/pull/675
- Clients try HTTPS first. A server behind a reverse proxy may be HTTPS only.
  Over plain HTTP or a raw IP, the client asks the user to trust the operator key on first use.
- A 2xx proves only the TCP signalling path. Game traffic is WebRTC over UDP on other ports.
  "Visible but cannot join" is an open bug: https://mojira.dev/BDS-23108
  Testing the media path needs an Xbox-signed SDP offer.
- Geyser supports external signalling hosts (for example `*.wdn.gg`).
  The signalling address can differ from the game host.

### B3. The IPv4/IPv6 port split

- No open Mojang ticket asks for one shared RakNet port. BDS-752 (2019) was closed as Invalid.
- server.properties docs: `server-portv6` "is ignored when transport=nethernet and a dual-stack socket will be opened on server-port":
  https://minecraft.wiki/w/Server.properties
- So with NetherNet, IPv6 uses the IPv4 port. The 19133-first order holds for RakNet only.
- Comments on BDS-23108 say NetherNet over IPv6 rarely works. Unverified.

### B4. Pong quirks (RakNet)

- BDS-23066 (open, 1.26.30 to 1.26.32 and later): with `enable-lan-visibility=false`, the pong ends after the magic.
  It is 33 bytes, with no string: https://mojira.dev/BDS-23066
  Today we report that as invalid data. It should count as online with no MOTD.
- gophertunnel and Dragonfly append `0;0;` after port6. Extra fields must be ignored.
- No format change announced.
- Cloudburst RakNet (Geyser, WaterdogPE) checks only the magic.
  It answers 0x01 and ignores 0x02. It needs no padding.
  Its per-IP limiter resets every 10 ms, so two tries are fine.

### B5. Bedrock formatting codes

- Official list: https://learn.microsoft.com/en-us/minecraft/creator/reference/content/rawmessagejson
- Hex values: https://minecraft.wiki/w/Formatting_codes (values current since 26.50).
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

- A redesigned Servers tab is rolling out in Bedrock Preview. UI only.
- No other change to how clients ping third-party servers.

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
  The 1.20.3 NBT chat change applies to play packets only.
- Vanilla 1.20.3+ and Adventure emit unstyled parts as plain strings. `extra` elements can be strings.
  We handle that.
- A translate-only MOTD was seen in the wild (mcstatus issue 319, 2022).
- Components without text (`object`) exist since 1.21.9.
- A top-level array is legal: the first element is the parent, the rest its extra. No mainstream server sends one.
- 26.1-pre2 (2026-03-13): the client cuts MOTD nesting deeper than 16 levels.
  https://minecraft.wiki/w/Text_component_format
- snake_case events (1.21.5) and `shadow_color` (1.21.4) do not change the visible text.

### J3. Ping/Pong and proxies

- No server requires the Ping (0x01).
- BungeeCord's connection throttle (default 3 per 4 s per IP) counts every connection.
  It lifts only after Ping/Pong, so probes that close early count against it.
- Velocity with ping-passthrough holds the status until its backend pings finish.
- TCPShield serves a cached MOTD.
- Vanilla times the RTT from the Ping to the Pong.

### J4. Protocol -1

- The convention per the wiki, which warns that some servers may close on an invalid version.
- Velocity shows its newest version. BungeeCord shows its own. Vanilla ignores the field.
- mcstatus, minecraft-server-util and mcutil send 47. Velocity then downsamples hex colours.
- go-mc sends its real version.

### J5. SRV and the client over IPv6

- No change: `_minecraft._tcp`, only without an explicit port. The client sends the SRV target, sometimes with a trailing dot.
- TCPShield shows "Invalid Hostname" when SRV points at its CNAME.
- No Happy Eyeballs in the client: MC-255735, MC-255720, still open in Jan 2026.
  The client tries only the first resolved address.

### J6. Fields and values to tolerate

- `enforcesSecureChat`, `previewsChat` (gone since 1.19.3), `preventsChatReports` (mod), `forgeData`, `modinfo`, `contact`.
- A missing `players` object.
- A snapshot protocol (26.4 Snapshot 1 is 1073742163), or a fake one from maintenance plugins.
- Section-sign codes in `version.name`.
- `enable-status=false`: vanilla sends no status at all.

## Open questions for the design

1. Bedrock: RakNet and `/v1/join` in parallel, or one after the other? Which answer wins when both answer?
2. Bedrock NetherNet over IPv6: which port, and how does that fit the port fallback?
3. `/v1/join`: HTTP first or HTTPS first? Verify certificates? Send the typed name as Host and SNI?
4. How to show "signalling works, game path unknown" for NetherNet.
5. The API field that names the scheme (RakNet, NetherNet, SLP). The cache key may need it.
6. Java: parse `id@host` and `?props` in the input. Look up only the host.
7. Java: add Ping/Pong for RTT, with the whole exchange as fallback?
8. Java: a TCP connect with no status: report "status disabled or ID required"?
9. Bedrock colours: a separate palette per edition in `mcText`.
10. Timeline: 26.60 is scheduled for 2026-10-27.
