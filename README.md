# mc_dualstack_check

Checks a Minecraft server over IPv4 and IPv6 separately, from a dual-stack host.
Supports Bedrock (RakNet ping, UDP) and Java (Server List Ping, TCP).
The own backend does the probing. A third-party provider (mcsrvstat.us) exists as an interim option.

![Icon](frontend/favicon.svg) Live Website: https://www.poggensee.it/mc_dualstack_check/

Backend API: https://mcdscheck-api.poggensee.it/

Free software under the GNU AGPL-3.0-or-later, see [LICENSE](LICENSE). Anyone who runs a modified copy, also as a network service, has to offer its source under the same terms.

## Design principles

**Thin backend, smart frontend.** The backend does only what the frontend cannot. It holds no state, no fallback logic, no rendering. Every request is short, so CPU time stays near zero wherever it runs. All logic (literal IP handling, port fallback order, per-family independence, the debug log, the UI) lives in the frontend: static files plus a relay on the web host, so the browser never talks to third parties. Changing behaviour means editing the page, not redeploying a service.

**Fully dual-stack.** Every hop is reachable over IPv4 and IPv6: the frontend host, the backend endpoint, and the probes. IPv4 and IPv6 are probed independently and never fall back to each other. The backend must run on a host with real IPv6 egress. A missing family on the checker host is reported as `no_route`, never as "offline", so the result is honest.

## Layout

- `backend/` - the backend service: `/ping` probes one address, `/health` reports its state. One static binary.
- `frontend/` - the page plus its relay to the backend. `providers.js` holds the backend adapters. `.htaccess` sets up the web host.
- `deploy/` - VM setup: systemd units, Caddy config, the release puller, the API landing page.
- `test/` - backend checks (CI) and live end-to-end checks.
- `docs/api.md` - API reference.
- `.github/workflows/` - CI on push, deploys on release.

## Providers

The browser only ever talks to the web host. The frontend relay resolves names itself and relays each probe to the provider chosen in the frontend config:

- `MC_PROVIDER = "own"`: the own backend at `MC_BACKEND`. The default.
- `MC_PROVIDER = "mcsrvstat"`: api.mcsrvstat.us, third-party. Answers cached up to 5 minutes on their side, and their IPv4 Bedrock path is unreliable at the time of writing. The page shows a notice.
- `MC_PROVIDER = ""`: the page renders, Check explains that no backend is configured.

`providers.js` maps each provider's answers to one result shape; the orchestration does not know which one is active.

## API

The public API is the backend, documented in [docs/api.md](docs/api.md): the endpoints, the result shapes, the 60 s cache, the internal-address filter and the limits. Per client, more than 10 systems per minute or more than 4 health requests within 7 s start a 60 s cooldown. The request budget is 20, refilled one per second. At most 64 probes run at once.

The frontend relay serves the page only, at `api/<endpoint>` on the web host:

- `api/config`: the provider and the release, `{"provider": "own" | "mcsrvstat" | "", "version": "..."}`.
- `api/resolve?host=<name>`: `{"a": [...], "aaaa": [...], "errors"?: {...}}`, looked up on the web host. An entry in `errors` means the lookup failed, which is never shown as a missing record.
- `api/ping` and `api/health`: passed to the provider. With `own`, the answer is the backend's, status code included.

The relay answers `404` for unknown endpoints, `502` when the provider is unreachable and `503` when none is configured.

Frontend behaviour: a literal IP skips DNS and omits the other family. Without "Disable port fallback" the edition default ports are retried; for Bedrock IPv6 that means 19133, then 19132. Per family the card shows one of: Online, Offline, No DNS record, DNS error, Omitted, Unavailable (no route from the checker). A 429 from the API is shown as a countdown.

## Development and build

The backend is written in Go. The frontend relay is PHP (`frontend/api.php`, settings in `frontend/config.php`). The page is plain JavaScript.

```bash
cd backend && go run .
```

```bash
cd frontend && php -S localhost:8000 api.php
```

`api.php` doubles as the router of the development server: it answers `api/<endpoint>` and serves the other files, scripts excepted. On the web host, `.htaccess` maps `api/<endpoint>` to it and hides `.php` files. PHP runs there as CGI, which needs `Options +ExecCGI`.

`frontend/config.php` points at `http://localhost:8080` by default. The frontend deploy overwrites it.

The backend does not probe internal addresses. To check a server on the local network, start it with `FILTER_INTERNAL_TARGETS=false`.

## Tests

- `sh test/backend.sh` starts the backend locally and checks endpoints, validation, name lookups, the internal-address filter, the cache and the limits. CI runs it on every push.
- `sh test/live.sh` checks the deployed web interface and API end to end (hostnames, IPv4 and IPv6 literals, Bedrock and Java, versions). The "Live check" workflow runs it after each frontend deploy, once it sees the released version on the VM, plus daily and on demand.

## Deployment

Both workflows run on a published GitHub release. Nothing secret lives in the repo.

### Backend (Linux VM)

The backend needs a host with native IPv4 and IPv6 egress. It runs on a cloud VM with a public IPv4 and a GUA IPv6, named `mcdscheck-api.poggensee.it` in DNS.

One-time setup on the VM:

```bash
sudo dnf -y install git && git clone https://github.com/poeggi/mc_dualstack_checker && sudo sh mc_dualstack_checker/deploy/bootstrap.sh
```

This installs the backend as a systemd service (user `mcdc`, port 8080), Caddy for TLS on 443 (Let's Encrypt), and the `mcdc-tick` timer. The cloud firewall and firewalld must allow TCP 443.

`mcdc-tick` runs every 7 minutes and does two things: it pulls the latest GitHub release and installs it if the tag changed (outbound only, no deploy credentials), and it keeps the CPU busy for 30 s. Some free-tier clouds reclaim VMs whose CPU looks idle for days; the burst keeps the CPU busy at about 7 % average load.

`mcdc-tick` only replaces the binary. After changes to `deploy/` (Caddy config, units, landing page), pull and rerun `bootstrap.sh`.

Caddy serves the API publicly plus a landing page from `deploy/www/`, and trusts forwarded client addresses from the web host only. `/health` reports the running version.

The release workflow builds `linux/amd64` and `linux/arm64` binaries and attaches them with `SHA256SUMS` to the release. The VM picks them up within 7 minutes.

### Frontend (FTPS to the web host)

Secrets: `FTP_HOST`, `FTP_USER`, `FTP_PASS`.
Variables: `FTP_TARGET_DIR` (`./` when the FTP user is jailed at the target folder), `MC_PROVIDER` (`own` or `mcsrvstat`), `MC_BACKEND` (own backend URL), `LIVE_URL` (optional, verifies the upload).

The workflow writes `MC_PROVIDER`, `MC_BACKEND` and the release tag as `MC_VERSION` into the frontend config before upload; the page shows it in the footer.

### Release flow

1. Merge to `main`, CI passes.
2. Create a GitHub release with a tag like `v1.0.0`.
3. The release workflow attaches the backend binaries; the frontend deploy uploads the page. Check the Actions tab.

The frontend deploy runs in the `production` environment. Add a required reviewer there to get a manual approval step.
