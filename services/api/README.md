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
the supported game catalog. The complete contract is in `openapi/openapi.yaml`.

Email OTPs are logged only when `EMAIL_MODE=log` for local development. Production
configuration rejects that mode and requires SMTP. Access tokens are short-lived;
refresh tokens rotate on every use and the underlying session remains valid until
the player explicitly logs out.

All mutations use the writer pool. Safe catalog and collection reads use the
reader pool and fall back to the writer when the reader is unavailable. Current
player reads use the writer to preserve read-after-write consistency during
onboarding.
