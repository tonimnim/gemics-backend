# Screenshot ingestion and production rollout

Updated 2026-09-28. The code supports scalable ingestion; 10,000-player capacity
has NOT been demonstrated against a deployed stack. Do not treat unit tests as a
capacity certification. The mobile app must adopt the OpenAPI 0.9.0 polling flow.
Screenshots are needed only for a final score report after a mismatch; see
[result verification](result-verification.md). Evidence is JPEG or PNG only: video
uploads are refused.

## Flow

1. Authenticated player requests an upload intent. Shared Redis atomically reserves
   an intent and declared bytes: defaults 30 intents/10 minutes, 512 MiB/day/player.
   A Redis outage returns retryable 503, never unlimited upload authorization.
2. Player PUTs directly to a private S3-compatible bucket. The signature binds
   exact Content-Length, Content-Type, SHA256 and `If-None-Match: *`. A repeated
   successful PUT cannot overwrite the original. Storage credentials never enter
   the mobile application. The API only handles small JSON requests.
3. Completion puts the evidence and one durable job into PostgreSQL in the same
   transaction. This is idempotent and returns `processing`, `ready=false`.
   There is a one-hour completion grace period after PUT URL expiry for interrupted
   mobile sessions; this does not extend the signed PUT URL's validity.
4. Separate worker containers claim jobs using SKIP LOCKED and a recoverable
   lease. No database connection is held during object download or image decoding.
   They verify provider metadata, re-hash bounded downloaded bytes, parse the
   JPEG/PNG structure before any decoder runs, check dimensions and the estimated
   decode size, and then fully decode the image. Originals remain unchanged.
5. A fenced transaction sets the evidence and job to completed/succeeded, rejected,
   or terminal failed. Transient failures retry with capped, jittered back-off until
   the job's retry window (5 minutes from queueing by default) runs out; the upload
   stays `processing` meanwhile and then fails with `verification_unavailable`. A
   crashed worker's job is reclaimed after its two-minute lease; a job whose lease
   has to be recovered more than twice fails with `processing_aborted`. An
   object that was never uploaded fails with `upload_missing` once its upload window
   plus a two-minute grace has passed. A stale worker cannot finalize a newer
   worker's claim.
6. Mobile polls with jitter (2 seconds rising to 10 seconds), resumes by evidence
   ID after restart, and attaches evidence only when `ready=true`. Only the uploader,
   and Gamics reviewers and admins without a conflict of interest in the match, can
   view a screenshot; opponents and organizers never can.

This verifies file integrity, not result authenticity. The blind dual score report
and Gamics review of claims that still differ remain the result controls. No OCR,
anti-cheat verdict or malware-scanner claim is made. PNG/JPEG decoding is bounded
and isolated in the worker container.

A screenshot stuck in Gamics' own pipeline never costs a player their place: if a
player's upload is still `processing`, or failed with `verification_unavailable` or
`processing_aborted`, when their response window ends, the match goes to Gamics
review instead of removing them. Rejected images and `upload_missing` are the
player's responsibility.

## Redis, storage and database roles

- Object storage holds image bytes. Do not put screenshots or base64 in Redis or
  PostgreSQL. Use a private managed service in production; local single-node
  MinIO is a development dependency, not a high-availability deployment.
- Redis coordinates budgets across API replicas. Use managed Redis with failover,
  persistence, `noeviction`, TLS and appropriate memory. The cache Redis should
  be a separate evictable service. Losing rate-limit state resets budgets but
  cannot lose verification jobs because jobs are in PostgreSQL.
- PostgreSQL writer owns metadata and the durable queue. No read replica is used
  for evidence ownership/completion/polling. Provision writer HA and backups;
  a read replica alone does not implement automatic failover.

## Capacity planning

10,000 images at 1 MB = approximately 10 GB (decimal). If that is daily, it adds
300 GB per 30 days before deletion. Uploaded over one minute it requires roughly
167 PUT/s and 1.33 Gbps upload throughput, before overhead. Verification also reads
all 10 GB once and HEADs every object. Private views add further requests/egress.

Scale API replicas by latency and CPU, workers by oldest queued-job age and
CPU/memory. Default worker concurrency is two; maximum accepted image dimensions
are 16 million pixels and 8192 per side, and an image whose estimated decode size
exceeds 96 MiB is rejected before decoding. 16-bit PNGs, progressive JPEGs and
CMYK JPEGs are rejected as `unsupported_image_encoding`. Decodes share a
per-process budget (`EVIDENCE_WORKER_DECODE_BUDGET_BYTES`, 192 MiB by default).
Compose sets 512 MiB/container and a 384 MiB Go soft limit (`GOMEMLIMIT`); the
worker refuses to start unless `concurrency x 25 MiB + decode budget + 64 MiB` fits
under it (306 MiB with the defaults). Raise memory before raising concurrency.
Production load tests must include maximum-size inputs as well as typical 1 MB
screenshots.

Avoid using one pod per upload. Keep worker concurrency bounded and add replicas.
Total database pool capacity must fit the writer budget across all API/worker
replicas. This worker uses at most min(concurrency, 8) connections per replica.

## Run locally

From the repository root after configuring `.env.docker`:

```powershell
docker compose --env-file .env.docker up --build -d
docker compose --env-file .env.docker up -d --scale evidence-worker=3
```

Migrations 000019 to 000021 must precede the new API rollout, and the new API must
precede the new worker, whose claim query needs migration 000021. In production run
migrations as a separate deployment job. The worker never applies migrations. Its HTTP port
8081 is internal only. `/healthz` checks DB connectivity; `/metrics` exposes queue
depth, oldest job age, terminal failures and per-process outcome counters.
Use MAX, not SUM, for queue gauges scraped from several replicas (each sees the
same queue). Alert on oldest age >60s, terminal failures, growing retries,
`gamics_evidence_errors_total` by class and worker absence. Logs carry only the
evidence id, status, bounded error code and class, attempt and timings; keep them
free of signed URLs and uploaded contents.

Deploy API, workers and the updated mobile polling flow together. Old completed
images remain completed; their contents are not retroactively inspected. New
HEIC/HEIF uploads must be converted on-device. Existing HEIC objects remain
readable. Video evidence is no longer accepted: migration 000021 fails any pending
or processing video upload with `video_unsupported`, and completing a legacy video
upload returns `422 evidence_media_unsupported`.

## Provider configuration and acceptance

Selected provider: private Cloudflare R2 Standard. Use `STORAGE_MODE=r2` and the
dedicated variables/Compose overlay in [Cloudflare setup](cloudflare-r2.md).
Endpoint, region and path style are derived automatically; keep `r2.dev` and
public custom domains disabled. The selection is implemented in configuration,
but live provider acceptance and capacity tests remain required.

Use bucket-scoped read/write credentials for the API and separate read-only
credentials for workers. The signer reads static environment credentials;
arrange secure rotation/restarts. `EVIDENCE_WORKER_DATABASE_URL` can use a role
limited to the evidence tables. The worker container receives no payment,
auth-signing, mail, push or Redis credentials. Do not connect a public CDN pull
zone to evidence. Existing objects must be migrated and verified before switching
a live database's storage provider; this change does not move them automatically.

Browser CORS needs exact app origins; PUT/GET/HEAD methods; Content-Type,
If-None-Match and x-amz-checksum-sha256 headers; and ETag exposure if used. Browsers
set Content-Length themselves for Blob bodies. Test native iOS/Android fixed-length
uploads against the selected provider as well.

Run the opt-in conformance test against a disposable private bucket. It writes
one tiny `conformance/` object and leaves it for that bucket's cleanup policy:

```powershell
powershell -File tools/Test-R2.ps1
```

The test checks checksum rejection, signed-length enforcement, duplicate PUT 412,
HEAD SHA256, private downloads, denied anonymous/expired reads and read-only worker
permissions. S3 compatibility does not guarantee these features. R2 must pass this
gate before player traffic is enabled; do not weaken validation to make it pass.

Optional live queue and Redis tests (dedicated test services only):

```powershell
# Configure GAMICS_TEST_DATABASE_URL and GAMICS_TEST_REDIS_URL in your shell.
go -C services/api test ./internal/evidence ./internal/httpapi -run 'TestQueueConcurrent|TestEvidenceBudgetConcurrent' -v
```

Use `tools/load/evidence.js` for a full staging flow. It uploads real fixture bytes,
creates real evidence records and consumes test-account allowances. Start with a
small smoke run, then 10,000 iterations at the intended arrival rate. Inspect k6
dropped iterations as well as error/latency metrics; the generator's bandwidth
must exceed the desired traffic. A test with 10,000 accounts is not proof of 10,000
simultaneous network connections. Repeat with worker restarts and temporary
storage/Redis outages, then verify the queue drains without duplicate finalization.

## Remaining deployment decisions

Choose a retention period covering the result review window, any later staff
investigation and account deletion requirements, then implement binding-aware
cleanup for abandoned/failed uploads. This change does not silently expire or
delete existing evidence. A blanket bucket TTL could delete the screenshots of a
match still in Gamics review and must not be applied casually.
Provision alerts, managed-service failover and backups, run live conformance and
load tests, and integrate the mobile polling flow before calling this production
ready. No cloud resources or paid accounts were provisioned by this change.
