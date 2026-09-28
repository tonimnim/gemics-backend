# Production-like local screenshot test

This is an isolated `gamics-loadtest` Docker Compose project. Its database is
`gamics_loadtest`; it does not reuse another project's volumes or users. It uses
the real private R2 staging bucket from `.env.r2`, not mock storage.

## Topology

- Nginx on `127.0.0.1:8080`, balancing two Go API containers.
- PostgreSQL 17 primary and streaming replica, on loopback ports 15432/15433.
- Separate Redis 8 security store (AOF, noeviction) and disposable response cache
  (128 MB, allkeys-lru). Security Redis is on loopback port 16379.
- Three screenshot workers, two jobs per worker, each limited to 512 MiB.
- One-shot migration command before API startup; API migrations disabled.
- Mailpit captures test email on localhost:18025. No real email or M-Pesa charges.
- API production configuration checks are enabled. App/API containers have
  explicit CPU/memory bounds. No public database or Redis ports are opened.

All containers and the Windows load generator still share one laptop and network.
Internal database/Redis/SMTP traffic and the local gateway are unencrypted here;
production needs TLS, managed failover, backups, secrets management and a public
HTTPS gateway. This does not test geographically distributed phones or host/zone
failure. Production readiness cannot be established from this test alone.

## Start and inspect

Use `.env.loadtest` for isolated random database/auth secrets and `.env.r2` for R2.
Both are ignored. Do not substitute a live production database. Docker Compose
2.24.4+ is needed for merge tags. If Docker is absent from PATH, use the CLI
installed with Docker Desktop (the current machine uses its per-user install).

```powershell
docker compose -p gamics-loadtest --env-file .env.loadtest --env-file .env.r2 -f compose.yaml -f compose.r2.yaml -f compose.loadtest.yaml build api
docker compose -p gamics-loadtest --env-file .env.loadtest --env-file .env.r2 -f compose.yaml -f compose.r2.yaml -f compose.loadtest.yaml up -d --scale api=2 --scale evidence-worker=3 gateway evidence-worker
docker compose -p gamics-loadtest --env-file .env.loadtest --env-file .env.r2 -f compose.yaml -f compose.r2.yaml -f compose.loadtest.yaml ps
```

The test overlay is not a production deployment manifest. Pin provider image
digests for actual deployments; record the resolved test images with results.

## Run the exact 2 MB flow

```powershell
go -C services/api run ./cmd/evidence-loadtest -players 1 -out ../../.loadtest/smoke.json
go -C services/api run ./cmd/evidence-loadtest -players 1000 -out ../../.loadtest/burst-1000.json
```

### Docker Desktop: avoid the Windows host-port bottleneck

The Windows-host run on 2026-09-28 reproduced TCP connection refusals before
requests reached Nginx/API. A 1,000-request health probe failed on Windows but
passed 1,000/1,000 inside this Docker network. Increasing the Nginx listener
backlog alone did not resolve the Windows-path failures. Run the generator inside
the network for the capacity test; this still shares the laptop's internet link.

Build the Linux generator (these environment values are for this shell only):

```powershell
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go -C services/api build -trimpath -o ../../.loadtest/evidence-loadtest-linux ./cmd/evidence-loadtest
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
$testArtifacts = Join-Path (Get-Location) '.loadtest'
$testEnv = Join-Path (Get-Location) '.env.loadtest'
$testR2 = Join-Path (Get-Location) '.env.r2'

docker run --rm --no-healthcheck --network gamics-loadtest_default --mount "type=bind,source=$testArtifacts,target=/work/.loadtest,readonly" --entrypoint /work/.loadtest/evidence-loadtest-linux gamics-loadtest-api:local -docker-network -api http://gateway:8080 -gateway-probe

docker run --rm --no-healthcheck --name gamics-loadtest-generator --network gamics-loadtest_default --memory 768m --cpus 2 --mount "type=bind,source=$testArtifacts,target=/work/.loadtest" --mount "type=bind,source=$testEnv,target=/work/.env.loadtest,readonly" --mount "type=bind,source=$testR2,target=/work/.env.r2,readonly" --workdir /work --entrypoint /work/.loadtest/evidence-loadtest-linux gamics-loadtest-api:local -docker-network -api http://gateway:8080 -config /work/.env.loadtest -r2-config /work/.env.r2 -players 1000 -out /work/.loadtest/burst-1000-docker.json -timeout 10m
```

The explicit Docker mode permits only `http://gateway:8080` and the `postgres`
service on the isolated network. It still requires database `gamics_loadtest` and
a staging bucket. Do not attach this generator to a production network. Neither
mode retries failed PUTs. Failure categories expose numeric socket errors, never
signed URLs. A client transport failure can leave an object whose acknowledgement
was lost; do not interpret the confirmed-upload count as an exact bucket inventory.
The one-shot generator does not run an API listener; `--no-healthcheck` disables
the API image's inherited HTTP health check for this process only.

### SQL completion regression

`TestEvidenceCompletionAtomicAndIdempotent` requires
`GAMICS_TEST_DATABASE_URL` and `GAMICS_TEST_REDIS_URL` pointing at test services.
Use Redis database 15 and the isolated PostgreSQL instance above. The test creates
and drops its own random PostgreSQL schema and deletes only its exact Redis key.
It checks the real SQL preparation, duplicate/concurrent completion, one durable
job and outbox event, and rollback of all writes when the outbox insert fails.

```powershell
go -C services/api test ./internal/httpapi -run '^TestEvidenceCompletionAtomicAndIdempotent$' -count=1 -v
```

Each image is exactly **2,000,000 bytes**, so 1,000 uploads are **2,000,000,000
bytes (2 GB decimal)**. The fixture is a genuine decodable synthetic PNG with a
valid padding chunk, not an actual gameplay screenshot. Every upload receives a
unique evidence ID and object key. Verification workers also download the bytes,
adding approximately 2 GB of download traffic for a successful full run.

The tool seeds distinct active test accounts and refresh sessions in the isolated
database, then issues real signed access tokens. It does not bypass API session
checks or Redis evidence allowances. It skips OTP delivery/onboarding as a setup
step; those features are not part of the capacity result.

All players request intents concurrently. Once that phase completes, all prepared
uploads are released at one barrier. Each player then completes through the API
and polls, with staggered backoff, until worker-verified `ready=true`. There are no
automatic PUT retries to hide first-attempt errors. Polling 429/503 is retried.

Reports contain status counts, failures, percentile timings, dispatch skew,
observed request/connection overlap and PostgreSQL queue samples. They contain
neither tokens nor presigned URLs. Peak in-flight calls alone is not proof that
all 1,000 network connections were acquired simultaneously; inspect both metrics.
Readiness timing includes polling delay. The default total deadline is 10 minutes,
with a 5-minute per-upload deadline; timeouts are reported, not called a pass.
`requested_payload_bytes` is the full player target; `attempted_bytes` is the
declared payload of PUTs actually launched. Neither measures retransmitted or
partially transmitted wire bytes. `successful_upload_bytes` counts only PUTs
acknowledged successfully. The first historical Windows report predates the
requested/attempted split; its caveat is recorded in the dated results document.

Test accounts, evidence records and R2 images are retained for inspection. Do not
apply blanket deletion to the staging bucket or wipe volumes. Any cleanup should
be limited to the report's exact test users/evidence IDs and verified object keys.

Stop only this stack, preserving volumes:

```powershell
docker compose -p gamics-loadtest --env-file .env.loadtest --env-file .env.r2 -f compose.yaml -f compose.r2.yaml -f compose.loadtest.yaml down
```
