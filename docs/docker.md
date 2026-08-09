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

The API automatically applies embedded, ordered migrations to the writer while
holding a PostgreSQL advisory lock. The replica receives those changes through WAL.

Useful endpoints:

- API liveness: `http://localhost:8080/healthz`
- dependency readiness: `http://localhost:8080/readyz`
- marketing website: `http://localhost:3000`

With `EMAIL_MODE=log`, request an email OTP and read it from:

```sh
docker compose --env-file .env.docker logs api
```

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
TLS PostgreSQL URLs and managed HA endpoints. Never publish PostgreSQL or Redis
ports publicly. Docker Compose remains a single-host environment and cannot
guarantee zero downtime; see `production-architecture.md`.
