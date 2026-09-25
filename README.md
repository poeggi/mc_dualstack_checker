# Minecraft Dualstack Checker

Checks a Minecraft server over IPv4 and IPv6 separately, from a dual-stack host.
Supports Bedrock (RakNet ping, UDP) and Java (Server List Ping, TCP).
A backend on a dual-stack VM does the probing.

![Icon](frontend/favicon.svg) Live Website: 
https://www.poggensee.it/mc_dualstack_check/

Live Backend Service API: 
https://mcdscheck-api.poggensee.it/

Free software under the GNU AGPL-3.0-or-later, see [LICENSE](LICENSE). Anyone who runs a modified copy, also as a network service, has to offer its source under the same terms.

## Design principles

**Thin backend, smart frontend.** The backend does only what the frontend cannot. It keeps nothing but a short in-memory result cache and the rate limits: no stored data, no fallback logic, no rendering. Every request is short, so CPU time stays near zero wherever it runs. All logic (literal IP handling, port fallback order, per-family independence, the debug log, the UI) lives in the frontend: static files plus a relay on the web host, so the browser never talks to third parties. Changing behaviour means editing the page, not redeploying a service.

**Fully dual-stack.** Every hop is reachable over IPv4 and IPv6: the frontend host, the backend endpoint, and the probes. IPv4 and IPv6 are probed independently and never fall back to each other. The public backend runs on a host with native IPv4 and IPv6 egress. If you deploy your own, make sure its host has both. A missing family on the checker host is reported as `no_route`, never as "offline", so the result is honest.

## Layout

- `backend/` - the backend service: `/ping` probes one address, `/health` reports its state. One static binary.
- `frontend/` - the page plus its relay to the backend. `.htaccess` sets up the web host.
- `deploy/` - VM setup: systemd units, Caddy config, the release puller, the API landing page.
- `test/` - backend checks (CI) and live end-to-end checks.
- `docs/api.md` - API reference.
- `.github/workflows/` - CI on push, deploys on release.

## API

The public API is the backend, documented in [docs/api.md](docs/api.md): the endpoints, the result shapes, the 60 s cache, the name lookup rules, the internal-address filter and the limits. Limits apply per IPv4 address or IPv6 /64. More than 10 systems per minute or more than 4 health requests within 7 s start a 60 s cooldown. The request budget is 20, refilled one per second. At most 128 probes and name lookups run at once.

The browser only ever talks to the web host. The frontend relay serves the page only, at `api/<endpoint>`:

- `api/config`: `{"backend": true | false, "version": "..."}`, whether a backend is configured and the release.
- `api/ping`: passed to the backend at `MC_BACKEND` with the client's address, the answer and status code unchanged. Only the parameters of `/ping` are passed on.
- `api/health`: one copy of the backend's `/health` for all visitors, refreshed at most every 7 s and asked for as the web host. Visitors' reloads never count against the backend's health limit.

The relay answers `404` for unknown endpoints, `502` when the backend is unreachable and `503` when none is configured. It does no DNS itself.

Frontend behaviour: a literal IP skips DNS and omits the other family. A name is resolved by the backend, per family. Without "Disable port fallback" the edition default ports are retried; for Bedrock IPv6 that means 19133, then 19132. Per family the card shows one of: Online, Offline, Unreachable (rejected on the way), No DNS record, DNS error, Omitted, Unavailable (no route from the checker). Unreachable wins over Offline when no port answers and at least one was rejected. A 429 from the API is shown as a countdown. While the backend's copy of a probe answer is younger than 30 s, half its cache time, the page answers that probe itself. The answer looks exactly like the backend's: cached, with the age it has by then. It comes after 100 ms, so the check still shows its brief loading state. The page keeps the backend's health for 30 s per tab.

## Development and build

The backend is written in Go. The frontend relay is PHP (`frontend/api.php`, settings in `frontend/config.php`). The page is plain JavaScript.

```bash
cd backend && go run .
```

```bash
cd frontend && php -S localhost:8000 api.php
```

`api.php` doubles as the router of the development server: it answers `api/<endpoint>` and serves the other files, scripts excepted. On the web host, `.htaccess` maps `api/<endpoint>` to it and hides `.php` files. It also makes browsers revalidate the page, script and stylesheet on every load, so a cached page never meets a newer stylesheet. PHP runs there as CGI, which needs `Options +ExecCGI`.

`frontend/config.php` points at `http://localhost:8080` by default. The frontend deploy overwrites it.

The backend listens on `127.0.0.1` only. It does not probe internal addresses. To check a server on the local network, start it with `FILTER_INTERNAL_TARGETS=false`.

## Tests

- `cd backend && go test ./...` checks how failed probes are classified. CI runs it on every push.
- `sh test/backend.sh` starts the backend locally and checks endpoints, validation, name lookups, the internal-address filter, the cache and the limits. CI runs it on every push.
- `sh test/live.sh` checks the deployed web interface and API end to end (hostnames, IPv4 and IPv6 literals, Bedrock and Java, versions). The "Live check" workflow runs it after each frontend deploy, once it sees the released version on the VM, plus daily and on demand.

## Deployment

Both workflows run on a published GitHub release.

### Backend (Linux VM with systemd)

The backend needs a host with native IPv4 and IPv6 egress: a public IPv4 and a global IPv6 address. 
A live instance of the service runs as `mcdscheck-api.poggensee.it`.

One-time setup on the VM, with git installed:

```bash
git clone https://github.com/poeggi/mc_dualstack_checker && sudo sh mc_dualstack_checker/deploy/bootstrap.sh
```

This installs the backend as a systemd service (user `mcdc`, port 8080 on loopback), Caddy for TLS on 443 (Let's Encrypt), and the `mcdc-tick` timer. It installs missing tools with the distribution's package manager and opens TCP 80 and 443 in firewalld or ufw when one is active. A cloud firewall in front of the VM must allow them too.

`mcdc-tick` runs every 7 minutes as root and does four things:

- It keeps Caddy at the version pinned in the script.
- It installs the latest GitHub release whenever its `SHA256SUMS` changes, so a release published again under the same tag counts too. That covers the backend binary and, from `deploy.tar.gz`, the Caddy config, the landing page, the systemd units and `mcdc-tick` itself. Outbound only, no deploy credentials.
- It keeps Caddy's `trusted_proxies` at the current addresses of the web host's DNS name, the `# trusted-host` line in `deploy/Caddyfile`. It resolves the name through the VM's normal resolver and reloads Caddy only when the addresses change; if the name does not resolve, the addresses stay. The addresses in the Caddyfile are the fallback.
- It keeps the CPU busy for 30 s, about 7 % average load. Some free-tier clouds reclaim VMs whose CPU looks idle for days. To turn this off, set `Environment=BUSY_SECONDS=0` in a drop-in (`systemctl edit mcdc-tick`); releases replace the unit file itself, not drop-ins.

So `bootstrap.sh` runs once per VM. Changes to `deploy/` arrive with the next release. A release can change what runs as root on the VM, so whoever can publish releases controls the VM.

Caddy serves the API publicly plus a landing page from `deploy/www/`, and trusts forwarded client addresses from the web host only. If the web host's addresses change, that trust follows its DNS name within about 7 minutes. `/health` reports the running version.

The release workflow builds `linux/amd64` and `linux/arm64` binaries, packs `deploy/` into `deploy.tar.gz`, and attaches them with `SHA256SUMS` to the release. The VM picks them up within 7 minutes.

### Frontend (FTPS to the web host)

Secrets: `FTP_HOST`, `FTP_USER`, `FTP_PASS`.
Variables: `FTP_TARGET_DIR` (`./` when the FTP user is jailed at the target folder), `MC_BACKEND` (backend URL; empty disables checks), `LIVE_URL` (optional, verifies the upload).

The workflow writes `MC_BACKEND` and the release tag as `MC_VERSION` into the frontend config before upload; the page shows the version in the footer.

### Release flow

1. Merge to `main`, CI passes.
2. Create a GitHub release with a tag like `v1.0.0`.
3. The release workflow attaches the backend binaries and the deploy files; the frontend deploy uploads the page. Check the Actions tab.

The frontend deploy runs in the `production` environment.
