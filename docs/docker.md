# Docker development topology

The React Native application runs on a phone or simulator. Docker provides the
marketing website, Go API, PostgreSQL writer/reader topology and Redis.

## Start the stack

Copy `.env.docker.example` to `.env.docker` and replace every example database,
replication, Redis and authentication secret before using the stack outside a
developer laptop:

```sh
docker compose --env-file .env.docker up --build
```

The stack contains:

- PostgreSQL primary on port `5432`;
- a physical streaming read replica on port `5433`;
- Redis with AOF persistence on port `6379`;
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

With `EMAIL_MODE=log`, request an email OTP and read it from:

```sh
docker compose --env-file .env.docker logs api
```

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
when you intentionally want to delete all local PostgreSQL and Redis data.

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
shares one Redis only for convenience. Never publish PostgreSQL or Redis ports
publicly. Docker Compose remains a single-host environment and cannot guarantee
zero downtime; see `production-architecture.md`.
