# mc_dualstack_check

Checks a Minecraft server over IPv4 and IPv6 separately.
Supports Bedrock (RakNet ping, UDP) and Java (Server List Ping, TCP).
No third-party lookup service is involved.

Live: https://www.poggensee.it/mc_dualstack_check/

## Layout

- `backend/` - Go service. Resolves A and AAAA, pings each address, returns JSON.
- `frontend/` - static page. Calls the backend with `fetch` and renders the cards.
- `.github/workflows/` - CI on push, deploys on release.

## API

`GET /check?host=<name|ip>&edition=bedrock|java&port4=<n>&port6=<n>&nofallback=1`

- `edition` defaults to `bedrock`.
- `port4` defaults to the edition default (19132 / 25565).
- `port6` defaults to `port4`.
- Without `nofallback` the edition default ports are retried. For Bedrock IPv6 that includes 19133.

Each family result has a `state`: `online`, `offline`, `no_dns`, `omitted` (literal IP of the other family) or `no_route` (the checker host has no connectivity for that family).

`GET /healthz` reports `{"ok":true,"ipv6":<bool>}`. `ipv6` tells whether the host has a global IPv6 address.

Requests are rate limited per client IP (burst 5, then one per 3 s). Over the limit the backend answers 429 with `Retry-After`.

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

Secrets: `FTP_HOST`, `FTP_USER`, `FTP_PASSWORD`.
Variables: `FTP_TARGET_DIR` (for example `/mc_dualstack_check/`), `MC_API_BASE` (the Cloud Run service URL).

The workflow writes `MC_API_BASE` into `frontend/config.js` before upload.

### Release flow

1. Merge to `main`, CI passes.
2. Create a GitHub release with a tag like `v1.0`.
3. Both deploy workflows run. Check the Actions tab.

Put both deploy workflows in the `production` environment so they can require a manual approval.
