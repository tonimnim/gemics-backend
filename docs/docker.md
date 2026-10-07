# Docker development topology

The React Native application runs on a phone or simulator. Docker provides the
marketing website, Go API, PostgreSQL writer/reader topology, Redis and private
S3-compatible object storage.

## Start the stack

Copy `.env.docker.example` to `.env.docker` and replace every example database,
replication, Redis, MinIO and authentication secret before using the stack
outside a developer laptop:

```sh
docker compose --env-file .env.docker up --build
```

The stack contains:

- PostgreSQL primary on port `5432`;
- a physical streaming read replica on port `5433`;
- Redis with AOF persistence on port `6379`;
- private MinIO S3-compatible storage on port `9000`, with its local console on
  loopback-only port `9001`;
- the Go API with separate writer/reader pools on port `8080`; and
- the marketing website on port `3000` by default.

With `RUN_MIGRATIONS=true`, the local API applies checksummed, embedded migrations
while holding a session-pinned PostgreSQL advisory lock. The replica receives those
changes through WAL. Production API replicas set `RUN_MIGRATIONS=false`; a single
deployment job runs migrations through a direct PostgreSQL connection before the
rolling API deployment.

Useful endpoints:

- API liveness: `http://localhost:8080/healthz`
- dependency readiness: `http://localhost:8080/readyz`
- marketing website: `http://localhost:3000`
- MinIO console: `http://localhost:9001`

With `EMAIL_MODE=log`, email codes (verifying an added email, password reset) are
read from:

```sh
docker compose --env-file .env.docker logs api
```

## Cloudflare R2 storage

Cloudflare R2 is the selected managed storage provider. Configure `.env.r2` and run
`npm run stack:r2:up` to merge `compose.r2.yaml` with the base stack. This uses
separate API/read-only worker credentials and removes the MinIO startup dependency
without deleting local volumes. Requires Compose 2.24.4 or newer. Follow the
[R2 setup and live acceptance test](cloudflare-r2.md) before enabling uploads.
Use `npm run stack:r2:down` to stop this variant without deleting volumes.

## Local evidence and avatar storage

Compose enables `STORAGE_MODE=s3`, starts MinIO with persistent storage and runs
the `minio-init` one-shot service to create a private bucket before the API
starts. Two endpoints are intentionally different:

- `STORAGE_S3_ENDPOINT=http://minio:9000` is private Compose DNS. The API uses
  it for `HEAD` requests that verify size and provider-validated SHA-256 data.
- `STORAGE_S3_PUBLIC_ENDPOINT` is placed in host-bound, presigned PUT/GET URLs
  returned to the client. When it is blank, Compose derives
  `http://localhost:<MINIO_API_PORT>`.

The public value must be reachable from the device receiving the signed URL.
Use the matching value in `.env.docker`:

```text
# Android emulator
STORAGE_S3_PUBLIC_ENDPOINT=http://10.0.2.2:9000

# iOS simulator
STORAGE_S3_PUBLIC_ENDPOINT=http://127.0.0.1:9000

# Physical phone on the same Wi-Fi
MINIO_API_BIND_ADDRESS=0.0.0.0
STORAGE_S3_PUBLIC_ENDPOINT=http://<computer-lan-ip>:9000
```

For a physical phone, allow inbound TCP `9000` through the development
computer's firewall. The S3 API binds only to `127.0.0.1` by default; setting
`MINIO_API_BIND_ADDRESS=0.0.0.0` is an explicit LAN opt-in. The administrator
console stays loopback-only. Do not change `STORAGE_S3_ENDPOINT` to the LAN
address; the API should continue using private service DNS. If only the public
endpoint changes, recreate the API with:

```sh
docker compose --env-file .env.docker up -d --force-recreate api
```

If `MINIO_API_BIND_ADDRESS` or `MINIO_API_PORT` changes, use the same port in an
explicit public endpoint and recreate the published MinIO port plus its
dependents:

```sh
docker compose --env-file .env.docker up -d --force-recreate minio minio-init api
```

MinIO root credentials are local infrastructure credentials. Never put them in
the React Native bundle, an endpoint URL or logs. Presigned URLs are temporary
bearer credentials and should also be redacted from logs. The bucket initializer
keeps anonymous access disabled and creates/updates the separate API user from
`STORAGE_S3_ACCESS_KEY` and `STORAGE_S3_SECRET_KEY`. The initializer refuses to
reuse the root username. Keep the secret key out of the client; SigV4 presigned
URLs necessarily contain the non-secret access-key identifier, temporary
signature and expiry. MinIO requires the application secret to contain 8-40
characters. The API user receives only GET/PUT access to the configured bucket;
it cannot list or access other buckets.

## Screenshot verification workers

Compose also starts `evidence-worker` with bounded concurrency and memory. After
upload completion the API returns `processing`; the mobile app must poll until
`ready=true`. Scale independently with
`docker compose --env-file .env.docker up -d --scale evidence-worker=3`.
See [screenshot pipeline](screenshot-pipeline.md) for rollout requirements and
[provider research](storage-provider-research.md) for S3, R2 and Bunny tradeoffs.

## Daraja sandbox

M-Pesa defaults to `MPESA_ENVIRONMENT=disabled`. To exercise STK Push, create a
Daraja sandbox app and put its consumer key, secret, sandbox shortcode and passkey
in the untracked `.env.docker`. Set `MPESA_ENVIRONMENT=sandbox`, generate a random
URL-safe callback token of at least 32 characters, and expose the API through a
public HTTPS origin such as a controlled development tunnel:

```text
MPESA_CALLBACK_BASE_URL=https://your-public-api-origin.example
```

The API constructs
`https://your-public-api-origin.example/v1/payments/mpesa/callback/<token>`.
Do not put the path in `MPESA_CALLBACK_BASE_URL`. Do not enable production mode
until Safaricom has approved the app, PayBill/Till and callback. Full safeguards
and required variables are in `mpesa-daraja.md`.

Stop containers with `docker compose --env-file .env.docker down`. Add `-v` only
when you intentionally want to delete all local PostgreSQL, Redis and MinIO data.

## Mobile API access

- Android emulator: `EXPO_PUBLIC_API_URL=http://10.0.2.2:8080`
- iOS simulator: `EXPO_PUBLIC_API_URL=http://127.0.0.1:8080`
- physical phone on the same Wi-Fi: `EXPO_PUBLIC_API_URL=http://<computer-lan-ip>:8080`
- production: `EXPO_PUBLIC_API_URL=https://api.gamics.io`

The phone cannot use Compose service names such as `http://api:8080`; those names
resolve only between containers.

## Production boundary

Production must set `APP_ENV=production`, `EMAIL_MODE=smtp`, SMTP credentials,
TLS PostgreSQL URLs and managed HA endpoints. Use separate managed Redis endpoints:
a no-eviction security store and a bounded disposable response cache. Local Compose
shares one Redis only for convenience. Production object storage requires HTTPS
internal and public origins, a private bucket and scoped application credentials;
do not reuse MinIO root credentials. Never publish PostgreSQL or Redis ports
publicly. Docker Compose remains a single-host environment and cannot guarantee
zero downtime; see `production-architecture.md`.
