# mc_dualstack_check

Checks a Minecraft server over IPv4 and IPv6 separately, from a dual-stack host.
Supports Bedrock (RakNet ping, UDP) and Java (Server List Ping, TCP).
No third-party lookup service is involved.

Live: https://www.poggensee.it/mc_dualstack_check/

## Design principles

**Thin backend, smart frontend.** The backend does only what a browser cannot: resolve a name and send one probe packet. It holds no state, no fallback logic, no rendering. Every request is short, so CPU time on Cloud Run stays near zero. All logic (literal IP handling, port fallback order, per-family independence, the debug log, the UI) lives in the frontend and ships as static files. Changing behaviour means editing JavaScript, not redeploying a service.

**Fully dual-stack.** Every hop is reachable over IPv4 and IPv6: the frontend host, the backend endpoint, and the probes. IPv4 and IPv6 are probed independently and never fall back to each other. The backend must run on a host with real IPv6 egress. A missing family on the checker host is reported as `no_route`, never as "offline", so the result is honest.

## Layout

- `backend/` - Go service with two primitives, `/resolve` and `/ping`.
- `frontend/` - static page. Orchestrates the check and renders the cards.
- `.github/workflows/` - CI on push, deploys on release.

## API

`GET /resolve?host=<name>` -> `{"a":[...],"aaaa":[...]}`

`GET /ping?ip=<addr>&port=<n>&edition=bedrock|java[&host=<name>]` -> one probe.
`host` is only sent in the Java handshake; some proxies route on it.

Ping `state` is `online`, `offline` or `no_route` (the checker host has no connectivity for that family). `online` carries `rtt_ms` and `info` (MOTD, version, protocol, players, and for Bedrock gamemode, map, server ID).

`GET /healthz` reports `{"ok":true,"ipv6":<bool>}`. `ipv6` tells whether the host has a global IPv6 address.

Requests are rate limited per client IP (burst 15, then one per second). A full check costs one resolve plus up to three pings per family. Over the limit the backend answers 429 with `Retry-After`.

Frontend behaviour: a literal IP skips DNS and omits the other family. Without "Disable port fallback" the edition default ports are retried; for Bedrock IPv6 that includes 19133.

## Local development

```bash
cd backend && ALLOWED_ORIGINS=http://localhost:8000 go run .
```

```bash
cd frontend && python -m http.server 8000
```

`frontend/config.js` points at `http://localhost:8080` by default.

## Deployment

Both deploy workflows run on a published GitHub release. Nothing secret lives in the repo.

### Backend (Cloud Run)

One-time setup. Cloud Run needs Direct VPC egress into a dual-stack subnet for IPv6.

```bash
gcloud services enable run.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com compute.googleapis.com
gcloud compute networks create mc-check --subnet-mode=custom
gcloud compute networks subnets create mc-check-sub --network=mc-check --region=REGION \
  --range=10.10.0.0/24 --stack-type=IPV4_IPV6 --ipv6-access-type=EXTERNAL
gcloud projects add-iam-policy-binding PROJECT \
  --member=serviceAccount:service-PROJECT_NUMBER@serverless-robot-prod.iam.gserviceaccount.com \
  --role=roles/compute.publicIpAdmin
gcloud run deploy mc-dualstack-check --source backend --region REGION --allow-unauthenticated \
  --network=mc-check --subnet=mc-check-sub --vpc-egress=private-ranges-only \
  --set-env-vars ALLOWED_ORIGINS=https://www.poggensee.it
```

Then verify: `curl SERVICE_URL/healthz` must show `"ipv6":true`, and a check against a dual-stack host must not return `no_route`.
If IPv6 stays unreachable, switch to `--vpc-egress=all-traffic` and add Cloud NAT for IPv4.
The network settings stick to the service; later deploys from the workflow keep them.

GitHub Actions authenticates with Workload Identity Federation (no key file).

Secrets: `GCP_WIF_PROVIDER`, `GCP_SA_EMAIL`.
Variables: `GCP_PROJECT`, `GCP_REGION`, `ALLOWED_ORIGINS`.

### Frontend (FTPS to the web host)

Secrets: `FTP_HOST`, `FTP_USER`, `FTP_PASS`.
Variables: `FTP_TARGET_DIR` (`./` when the FTP user is jailed at the target folder), `MC_API_BASE` (the Cloud Run service URL), `LIVE_URL` (optional, verifies the upload).

The workflow writes `MC_API_BASE` into `frontend/config.js` before upload. Unset, the page says that no backend is configured.

### Release flow

1. Merge to `main`, CI passes.
2. Create a GitHub release with a tag like `v1.0`.
3. Both deploy workflows run. Check the Actions tab.

Put both deploy workflows in the `production` environment so they can require a manual approval.
