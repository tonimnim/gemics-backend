# Gamics API

The API is a modular Go monolith backed by separate PostgreSQL writer and reader
pools plus Redis. Domain packages remain independent of transport and provider
details.

Run locally from the repository root:

```sh
npm run api:dev
```

The API is a nested Go module, so direct Go commands from the repository root
must use `go -C services/api ...`. Local startup also requires PostgreSQL and
Redis; the supported full-stack path is `npm run stack:up` after copying
`.env.docker.example` to the ignored `.env.docker` file and replacing its
placeholder secrets.

Cloudflare R2 is the selected managed evidence store. Set `STORAGE_MODE=r2` and
follow [R2 setup](../../docs/cloudflare-r2.md), including the live acceptance test
and `npm run stack:r2:up` overlay. Local MinIO remains available for development.

The API covers email OTP and rotating sessions; player onboarding, legal consent,
profiles, avatars and game-account verification; competition discovery, eligibility,
registration, typed draws and progression; rankings and public histories; match
check-in, blind score reports, private screenshot evidence, removal of silent
entries, the Gamics result review queue and conduct strikes; notifications;
organization RBAC; and protected payment/refund review. The M-Pesa routes reserve a
paid competition place, initiate Daraja STK Push, persist callbacks, confirm them
with STK Query and create the competition entry atomically with payment completion.
The complete machine-readable contract is `openapi/openapi.yaml`; mobile integration
order and deployment dependencies are documented in `../../docs/mobile-api-requirements.md`
and `../../docs/backend-api-status.md`.

Email OTPs are logged only when `EMAIL_MODE=log` for local development. Production
configuration rejects that mode and requires SMTP. Access tokens are short-lived;
refresh tokens rotate on every use, retain a bounded retry grace after a lost
response, and the underlying session remains valid until the player explicitly
logs out. Positive session-cache entries are capped at five seconds; logout does not
report success when Redis revocation propagation fails.

All mutations use the writer pool. Safe catalog and collection reads use the
reader pool and fall back to the writer when the reader is unavailable. Current
player reads use the writer to preserve read-after-write consistency during
onboarding.

`GET /v1/games` uses a reusable Redis stale-while-revalidate response cache with
stable ETags, process-local request coalescing, a distributed rebuild lock and a
bounded writer fallback. Production should use separate Redis services for
security/session state (`REDIS_SECURITY_URL`, no eviction) and disposable response
cache (`REDIS_CACHE_URL`, a measured memory limit and cache eviction policy).

Daraja is intentionally disabled until real credentials and a public HTTPS API
origin are provided. See `docs/mpesa-daraja.md`; never put provider secrets in a
mobile or web bundle.
