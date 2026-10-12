# Minecraft Dualstack Checker

Checks a Minecraft server over IPv4 and IPv6 separately, from a dual-stack host.
Supports Bedrock (NetherNet status over TCP and RakNet ping over UDP, raced) and Java (Server List Ping, TCP, with and without connection IDs).
A backend on a dual-stack VM does the probing. Both the frontend and the backend can be reached via either, IPv4 or IPv6.

![Icon](frontend/favicon.svg) Live Website: 
https://www.poggensee.it/mc_dualstack_check/

Live Backend Service API: 
https://mcdscheck-api.poggensee.it/

The live instance is free to use for non-commercial users or purposes only.

Free software under the GNU AGPL-3.0-or-later, see [LICENSE](LICENSE). Anyone who runs a modified copy, also as a network service, has to offer its source under the same terms.

Co-developed with Claude, Anthropic's AI assistant.

## Design principles

**Thin backend, smart frontend.** The backend does only what the frontend cannot. It keeps a short in-memory result cache, the rate limits and anonymous usage counts: no stored addresses, no fallback logic, no rendering. Every request is short, so CPU time stays near zero wherever it runs. All logic (literal IP handling, port fallback order, per-family independence, the failure log, the UI) lives in the frontend: static files plus a small helper on the web host. The browser talks only to the web host and to the API, never to third parties. The API sees visitors' addresses. Minecraft server names are looked up by the backend only, never in the browser. Changing behaviour means editing the page, not redeploying a service.

**Fully dual-stack.** Every hop is reachable over IPv4 and IPv6: the frontend host, the backend endpoint, and the probes. IPv4 and IPv6 are probed independently and never fall back to each other. The public backend runs on a host with native IPv4 and IPv6 egress. If you deploy your own, make sure its host has both. A missing family on the checker host is reported as `no_route`, never as "offline", so the result is honest.

## Layout

- `backend/` - the backend service: `/ping` probes one address, `/health` reports its state. It also serves the API host's pages from `www/`. One static binary, shipped as a container image (`Dockerfile`).
- `frontend/` - the page plus `api.php` for the web host. `.htaccess` sets up the web host.
- `test/` - backend checks (CI) and live end-to-end checks.
- `docs/api.md` - API reference.
- `.github/workflows/` - CI on push, deploys on release. `.github/stamp.sh` and `.github/image.sh` do the release's steps.
- `cosign.pub` - the public key the images are signed with.

## API

The public API is the backend, documented in [docs/api.md](docs/api.md): the endpoints, the result shapes, the 60 s cache, the name lookup rules, the internal-address filter and the limits. Limits apply per IPv4 address or IPv6 /64. At most 8 systems fit in a sliding minute. A 9th waits until the oldest leaves it, at least 7 s. More than 4 health requests within 7 s start a 60 s cooldown. The request budget is 16; 4 tokens come back every 7 s. At most 128 probes and name lookups run at once. Anonymous usage numbers are at `/stats` on the API host.

The page sends its probes straight to the API. The web host keeps one copy of the backend's `/health` for all visitors. A copy older than 7 s is refreshed after the answer has gone out, by one request only: 3 s per try, one retry when there is no answer. No visitor waits for the backend. The deploy keeps the copy.

The page is a static file. The deploy writes the API address and the page version into it. On load, the page asks `api/backend-health` once and shows the backend's version, or "API unavailable", in the footer.

`api/backend-health` answers with the copy, its age in the `Age` header. The page asks it on load and when a probe fails; anyone else may ask it too. It answers `502` when the web host could not reach the API, and `503` when none is configured or no copy exists yet. Other `api/` paths answer `404`. The web host looks up no server names itself.

When a probe cannot reach the API, or gets no answer within 12 s, the page asks `api/backend-health`. If that copy is older than 7 s, it asks once more after 6.5 s. If the web host cannot reach the API either, it shows "API unavailable." Otherwise it shows "The API is online, but your browser cannot reach it."

Frontend behaviour: a literal IP skips DNS and omits the other family. A name is resolved by the backend, per family. Without "Disable port fallback" the edition default ports are retried; for Bedrock IPv6 that means 19133, then 19132. NetherNet servers listen on one port for both families; the 19133 fallback serves RakNet servers. For Java at port 25565 the backend follows the name's SRV record, as Java clients do. The card then shows an SRV badge and a Redirect line, and no further port is tried. A Java address may carry a connection ID as `id@host`, as in the game. Server names and MOTDs keep their colours and line breaks, as each edition renders them; very dark ones are lightened. Scrambled text keeps changing, as in the game; with reduced motion it stays still. Java cards show the server icon next to the family. Under More, Bedrock cards name the transport that answered and list the ports the server announces. Java cards list the players the server names as online and whether it enforces secure chat. A Bedrock server that answers without details shows "No info provided"; vanilla servers do so with LAN visibility off. Per family the card shows one of: Online, Offline, Unreachable (rejected on the way), No DNS, Omitted, Unavailable (no route from the checker). A failed card has one status line per port tried: No response, Refused, Invalid data, Rejected, Failed, or for Java Connected, no status, with the detail in brackets. For Bedrock the failure log adds each transport's error. Unreachable wins over Offline when no port answers and at least one was rejected. A 429 from the API is shown as a countdown. While the backend's copy of a probe answer is younger than 30 s, half its cache time, the page answers that probe itself. The answer looks exactly like the backend's: cached, with the age it has by then. It comes after 100 ms, so the check still shows its brief loading state.

## Development and build

The backend is written in Go. The web host part is one PHP file, `frontend/api.php`. The page is plain HTML and JavaScript.

```bash
cd backend && ALLOWED_ORIGINS=http://localhost:8000 go run .
```

```bash
cd frontend && php -S localhost:8000 api.php
```

`api.php` doubles as the router of the development server: it answers `api/<endpoint>` and serves the other files, scripts excepted. On the web host, `.htaccess` maps `api/<endpoint>` to it and hides `.php` files by name. Browsers revalidate the page on every load; stylesheet, script and icons are cached for an hour and carry the release as a version stamp in their URLs, so a new release is a new URL. PHP runs there as CGI, which needs `Options +ExecCGI`.

The page's `data-api` attribute and `api.php` point at `http://localhost:8080` by default; the frontend deploy writes the live address into both. The page sends probes to that address from the browser, so the backend must allow the page's origin in `ALLOWED_ORIGINS`.

The backend listens on `127.0.0.1` only. It does not probe internal addresses. To check a server on the local network, start it with `FILTER_INTERNAL_TARGETS=false`. Local names such as `.lan` still stay unresolved; use the server's IP address.

## Tests

- `cd backend && go test ./...` checks how failed probes are classified, the race of the Bedrock transports, the NetherNet probe against fake plain and HTTPS servers, weak answers, the Java probe against a fake server (connection IDs, Ping and Pong, no status), the status and text component parser, text clipping and the usage counters.
- `sh test/backend.sh` starts the backend locally and checks endpoints, validation, connection IDs, the Bedrock transports against fake servers, name lookups, the internal-address filter, the cache and the limits.
- `sh test/live.sh` checks a deployed web interface and API end to end. It reads `WEB` (page URL), `API` (API URL), `LIVE_BEDROCK_HOST` (a dual-stack Bedrock server) and `LIVE_JAVA_HOST` (a Java server). Each part runs only when its setting is given.

CI runs the first two on pushes to main and on pull requests. The "Live check" workflow runs `test/live.sh` after each frontend deploy, once it sees the released version on the VM, plus daily and on demand. It takes `WEB` from the variable `LIVE_URL`, `API` from the variable `MC_BACKEND`, and the two server names from secrets of the same names.

## Deployment

Both workflows run on a published GitHub release.

### Backend (container image)

The backend needs a host with native IPv4 and IPv6 egress: a public IPv4 and a global IPv6 address. 
A live instance of the service runs as `mcdscheck-api.poggensee.it`.

Each release publishes the image `ghcr.io/poeggi/mc_dualstack_checker` for `linux/amd64` and `linux/arm64`. It is tagged with the version (`2.0.4`) and the major version (`2`). Following the major tag gets every release without breaking API changes. The image holds the backend and its pages, stamped with the release.

```bash
podman run -d --network host -v /var/lib/mc-dualstack-check:/state:U -e ALLOWED_ORIGINS='*' ghcr.io/poeggi/mc_dualstack_checker:2
```

- It needs the host's network: the probes use the host's IPv4 and IPv6.
- It listens on `127.0.0.1:8080` only. Put a reverse proxy with TLS in front that passes every path on and sets `X-Forwarded-For`.
- It sets its own response headers: security policy and caching.
- The usage numbers go to `/state`. Mount a directory there to keep them across updates.
- `/mc_dualstack_check -health` is the health check. It exits 0 when the running instance answers.

The images are signed with the key in `cosign.pub`. The sigstore signatures are stored next to the image. Podman accepts only signed images with two settings:

- `registries.d`: `use-sigstore-attachments: true` for the image.
- `policy.json`: a `sigstoreSigned` rule with `keyPath` pointing to `cosign.pub` and `signedIdentity` of type `matchRepository`. The signature names the version tag, so a host that pulls the major tag needs this.

The release workflow builds the image and runs it once. It pushes it signed and pulls it back through such a policy. A policy with another key must refuse it. Only then does the major tag move (`.github/image.sh`). CI runs the same steps against a local registry with a throwaway key.

The backend serves the landing page, the usage page and a `robots.txt` from `backend/www/`. The `robots.txt` asks all crawlers, AI crawlers included, to stay away; it is a request, the limits are what hold. Pages are revalidated on every load; their scripts, styles and icons are cached for an hour under version-stamped URLs. The usage files under `/stats/` come from the state directory. `/health` reports the running version.

### Frontend (FTPS to the web host)

Secrets: `FTP_HOST`, `FTP_USER`, `FTP_PASS`.
Variables: `FTP_TARGET_DIR` (optional, default `./` for an FTP user jailed at the target folder), `MC_BACKEND` (the API URL the page calls; empty disables checks), `LIVE_URL` (optional, the page URL with a trailing slash, verifies the upload).

The page paints server icons into a canvas. A Content-Security-Policy on the web host needs no `img-src` entry for them.

For the live check, optionally add the secrets `LIVE_BEDROCK_HOST` (a dual-stack Bedrock server that is always up) and `LIVE_JAVA_HOST` (a Java server that is always up). Without them, those checks are skipped.

The workflow writes `MC_BACKEND` and the release tag into the page and into `api.php` before upload: the footer version, the page's `data-` attributes and the version stamps of its includes. CI, the frontend deploy and the backend release refuse a page that includes a local file without a stamp, and a deploy stops when a stamp misses the release.

### Release flow

1. Merge to `main`, CI passes.
2. Create a GitHub release with a tag like `v1.0.0`.
3. The release workflow publishes the image; the frontend deploy uploads the page. Check the Actions tab.

The frontend deploy runs in the `production` environment.
