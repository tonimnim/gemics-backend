# Docker development and deployment

The Compose stack contains the marketing website, Go API and PostgreSQL. The
React Native application runs on a phone or simulator and connects to the API;
it is not a container service.

## Start the stack

Copy `.env.docker.example` to `.env.docker`, replace the example database
password in both `POSTGRES_PASSWORD` and `DATABASE_URL`, then run:

```sh
docker compose --env-file .env.docker up --build
```

The local endpoints are:

- marketing website: `http://localhost:3000`
- API: `http://localhost:8080`
- API health: `http://localhost:8080/healthz`
- PostgreSQL: `localhost:5432`

Stop containers with `docker compose --env-file .env.docker down`. Add `-v`
only when you intentionally want to delete the local PostgreSQL volume.

## Mobile API access

- Android emulator: `EXPO_PUBLIC_API_URL=http://10.0.2.2:8080`
- iOS simulator: `EXPO_PUBLIC_API_URL=http://127.0.0.1:8080`
- physical phone on the same Wi-Fi: `EXPO_PUBLIC_API_URL=http://<computer-lan-ip>:8080`
- production: `EXPO_PUBLIC_API_URL=https://api.gamics.io`

The phone cannot use Compose service names such as `http://api:8080`; those
names only resolve between containers.

## Production requirements

- Put the web and API behind TLS and a reverse proxy or managed load balancer.
- Do not publish PostgreSQL port 5432 publicly.
- Store secrets in the deployment platform, not in a committed env file.
- Restrict `CORS_ALLOWED_ORIGINS` to the real marketing/admin origins.
- Apply `services/api/migrations` as a separate release job before starting a
  new API version; automatic migrations are not wired yet.
- Store result evidence in private S3-compatible object storage using short-lived
  signed upload and download URLs. Do not mount screenshots into the API container.
