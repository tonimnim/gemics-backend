# Gamics API

The API is a modular Go monolith backed by separate PostgreSQL writer and reader
pools plus Redis. Domain packages remain independent of transport and provider
details.

Run locally from the repository root:

```sh
go run ./services/api/cmd/api
```

Implemented routes include email OTP authentication, rotating refresh sessions,
current-player onboarding, player profiles, game accounts, health/readiness and
the supported game catalog. The M-Pesa routes reserve a paid competition place,
initiate Daraja STK Push, persist callbacks, confirm them with STK Query and create
the competition entry in the same transaction as payment completion. The complete
contract is in `openapi/openapi.yaml`.

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
