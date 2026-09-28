# Cloudflare R2 setup

Selected on 2026-09-28 for private screenshot evidence. This configures R2 object
storage; the Go API, PostgreSQL, Redis and verification workers remain separate
services. The owner created the `gamics-evidence-stagin` bucket (that spelling is
intentional in the local configuration). On 2026-09-28 the live storage conformance
test passed against it using the supplied API and separate read-only worker keys.
One tiny test object remains under `conformance/`. The supplied API token has
account-wide admin permissions and the worker token has account-wide object-read
permissions; neither is yet restricted to this bucket as recommended below.
Native-client and browser CORS acceptance remain outstanding. The SQL completion
bug found by the first full-flow smoke test is fixed with a live PostgreSQL/Redis
regression. The 1-player and 10-player smoke tests pass. The 1,000-player burst
failed its all-player gate: all 1,000 intents succeeded inside Docker, but only
123 PUTs were acknowledged; all 123 were worker-verified. Production-scale
capacity is not established. See the full
[2026-09-28 readiness report](loadtest-2026-09-28.md).

The client obtains an authorized upload intent from the API, sends image bytes
directly to R2, completes the intent, then polls for verification. Redis limits
upload allowances across API replicas. PostgreSQL holds metadata and durable jobs;
bounded Go workers download and validate screenshots. R2 does not replace Redis
or the workers. See [the complete pipeline](screenshot-pipeline.md).

## 1. Create private buckets and credentials

In your Cloudflare account, enable R2 and create a Standard bucket. The example
production name is `gamics-evidence`; use a separate `gamics-evidence-staging`
bucket and separate credentials for testing. Keep both the public development
URL (`r2.dev`) and public custom domains disabled for these evidence buckets.
R2 location hints do not guarantee a Kenya location; measure uploads from Kenyan
mobile networks. [Cloudflare location documentation](https://developers.cloudflare.com/r2/reference/data-location/)

Create two R2 API tokens, restricted to the chosen bucket:

- API: **Object Read & Write**.
- Evidence workers: **Object Read only**.

Record each token's **Access Key ID** and **Secret Access Key**, plus the Cloudflare
account ID. These are S3 credentials, not a Global API Key or the bearer token
string. Bucket-scoped read/write permissions are not write-once storage; protect
the API credentials carefully. Client uploads are constrained by signed headers.
[Cloudflare token setup](https://developers.cloudflare.com/r2/api/tokens/)

## 2. Fill the private configuration

Use the ignored `.env.r2` in the repository root. It is now populated for the actual
`gamics-evidence-stagin` bucket; retain that exact name locally. The examples below
use the conventional `gamics-evidence-staging` spelling for a fresh setup.
For a fresh checkout, copy
`.env.r2.example` to `.env.r2` without replacing any existing secrets. Fill:

```dotenv
STORAGE_MODE=r2
R2_ACCOUNT_ID=<32-character-account-id>
R2_BUCKET=gamics-evidence-staging
R2_ACCESS_KEY_ID=<api-access-key-id>
R2_SECRET_ACCESS_KEY=<api-secret-access-key>
R2_WORKER_ACCESS_KEY_ID=<read-only-worker-access-key-id>
R2_WORKER_SECRET_ACCESS_KEY=<read-only-worker-secret-access-key>
R2_JURISDICTION=
```

Keep secrets out of chat, Git, logs and mobile environment variables. Use your
deployment secret manager in production. The current signer reads credentials at
startup; credential rotation requires restarting the affected API/worker services.

The loader derives the HTTPS endpoint from the account ID, selects region `auto`
and path-style addressing, and ignores old MinIO/AWS settings. Leave jurisdiction
empty for a default bucket; set `eu` only for a bucket actually created in that
jurisdiction. Other jurisdictions are not implemented in this configuration.
Presigned URLs use the R2 S3 endpoint, not a CDN/custom domain.
[S3 compatibility](https://developers.cloudflare.com/r2/api/s3/api/),
[presigned URLs](https://developers.cloudflare.com/r2/api/s3/presigned-urls/)

## 3. Run the storage acceptance test

From the repository root, with Go installed:

```powershell
powershell -File tools/Test-R2.ps1 -CheckOnly
powershell -File tools/Test-R2.ps1
```

The first command checks configuration without uploading. The second runs against
the bucket in `.env.r2`, writes a tiny object under `conformance/`, and leaves it
for explicit cleanup. A failed worker-permission check may leave another tiny
object. No database, Redis or Docker is needed for this provider test.

The live test requires rejection of wrong length/checksum, rejection of overwrite,
provider SHA256 on HEAD, authenticated private reads, anonymous-read denial,
expiry enforcement, and worker read access with PUT denied. It never prints the
credentials or signed URLs. It does not certify CORS, client compatibility or load.

**Passing this test is a release gate.** R2's published S3 compatibility matrix is
not proof of our precise PUT/HEAD checksum behavior. If it fails, investigate and
test an R2-specific adapter before enabling players; never replace a verified
checksum with client-supplied metadata or silently skip integrity checks.

## 4. Browser CORS, if needed

Native React Native requests are not subject to browser CORS. For browser uploads
or browser-based staff evidence views, edit `infra/cloudflare/r2-cors.example.json`
to contain only the actual allowed origins. It currently allows localhost:3000
for development. Do not retain development origins on a production bucket.

After authenticating Wrangler to the correct Cloudflare account, apply it to the
staging bucket (change the bucket argument for other environments):

```powershell
npx wrangler r2 bucket cors set gamics-evidence-staging --file infra/cloudflare/r2-cors.example.json
```

This is a bucket configuration change, not part of the test script. Use
`--jurisdiction eu` for an EU bucket. The example file uses Wrangler's JSON format;
the dashboard's S3-style CORS editor uses a different shape. CORS does not make a
private bucket public. Verify a real browser PUT/GET after configuring it.
[CORS documentation](https://developers.cloudflare.com/r2/buckets/cors/),
[Wrangler command reference](https://developers.cloudflare.com/r2/reference/wrangler-commands/)

## 5. Start the Docker stack with R2

Configure `.env.docker` for the database, Redis, authentication and other services
as described in [Docker setup](docker.md). Use Docker Compose **2.24.4 or newer**:
the overlay uses `!override` to remove the MinIO startup dependency.
[Compose merge behavior](https://docs.docker.com/reference/compose-file/merge/)

```powershell
npm run stack:r2:up
```

Equivalent command, including independent worker scaling:

```powershell
docker compose --env-file .env.docker --env-file .env.r2 -f compose.yaml -f compose.r2.yaml up --build -d --scale evidence-worker=3
```

The overlay selects R2, supplies separate worker credentials and does not start
MinIO. Existing MinIO volumes are preserved; an already-running MinIO service is
not automatically stopped. The local stack still runs single-host PostgreSQL and
Redis, so this command is not a high-availability production deployment.
Docker Desktop was subsequently located outside the shell PATH. An isolated
production-like R2 stack is now running; see the [test runbook](production-like-loadtest.md).
Migration `000019` must be applied before the new API and workers serve
traffic; use a separate migration job in production.

Outside Compose, inject the API R2 variables into the API service. For the worker,
inject its read-only values as `R2_ACCESS_KEY_ID` and `R2_SECRET_ACCESS_KEY`, plus
the shared account/bucket/jurisdiction and its database/worker settings. The
`R2_WORKER_*` names are overlay/test-script inputs, not worker runtime names.

**Existing evidence:** this configuration does not copy any objects. The backend
currently selects one storage provider globally. Before switching a live database
from MinIO/S3, migrate all referenced objects with their exact keys and checksums,
verify reads, and coordinate writes during cutover. Otherwise old evidence and
avatars will become inaccessible. Do not delete the source bucket during cutover.

## Mobile integration and production gate

The OpenAPI 0.7.0 upload flow is unchanged by the storage provider: request
intent, PUT raw bytes with all returned required headers, complete, poll
`ready=true`, then attach the evidence ID to a final score report. Evidence is JPEG
or PNG only; convert HEIC/HEIF before hashing. Do not use multipart/form-data for
the signed PUT. Browser Blob bodies and native upload libraries must send the
exact byte length. Signed URLs are bearer credentials; redact them from logs.

Before launch, run provider conformance, native iOS/Android and browser tests,
then staged burst tests using `tools/load/evidence.js`. Include worker restarts
and outages; verify queue recovery and drain time. Set alerts, backups, secret
rotation and a retention policy that protects the screenshots of matches still
in Gamics review. No automatic
evidence deletion is enabled by this change. Neither unit tests nor choosing R2
demonstrates support for 10,000 simultaneous uploaders.
