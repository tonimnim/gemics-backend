# Gamics

Competition and player infrastructure for African esports, beginning with
eFootball Mobile in Kenya.

## What is scaffolded

- An Expo SDK 57 React Native player app for iOS and Android.
- A responsive marketing website using the Next.js App Router programming model.
- A Go API with graceful shutdown, timeouts, structured logs, CORS and request IDs.
- Email OTP onboarding, rotating refresh sessions, player/game-account APIs and a
  replica-aware Redis-cached game catalog.
- A production-disabled Daraja foundation with STK Push/Query, durable callbacks,
  payment idempotency and atomic paid-entry creation.
- Competition lifecycle and result-submission domain models with tests.
- A PostgreSQL schema for organizers, solo/team entries, brackets, evidence,
  disputes, idempotency, audit history and transactional outbox delivery.
- An OpenAPI contract and an architecture rationale.

## Repository map

```text
app/                         Marketing website
apps/mobile/                 Expo React Native player app
services/api/cmd/api/        Go API entry point
services/api/internal/       Domain and transport modules
services/api/migrations/     PostgreSQL schema
services/api/openapi/        HTTP contract
docs/architecture.md         Boundaries, scale path and challenged decisions
docs/docker.md               Containers, API addresses and deployment notes
docs/result-verification.md  Screenshot, confirmation and dispute policy
docs/mobile-api-requirements.md  Implemented/planned mobile API boundary
docs/platform-readiness.md   Honest implementation and release-gap inventory
docs/production-architecture.md  Database, cache and network scale design
docs/security.md             Dependency audit baseline and release gate
```

## Local development

Requirements: Node.js 22+, npm, Go 1.26 and PostgreSQL 17+.

```sh
cp .env.example .env
npm install
npm run dev
```

For the player app, copy its environment example and start Expo:

```sh
cp apps/mobile/.env.example apps/mobile/.env
npm install --prefix apps/mobile
npm run mobile:start
```

In another terminal, start the API:

```sh
npm run api:dev
```

Checks:

```sh
npm run build
npm run lint
npm run mobile:typecheck
npm run mobile:lint
npm run api:test
```

The API starts on `http://localhost:8080`.

## Docker

Copy `.env.docker.example` to `.env.docker`, replace the example database
password in both values, then start the complete web, API and PostgreSQL stack:

```sh
docker compose --env-file .env.docker up --build
```

See `docs/docker.md` for emulator, physical-device and production API addresses,
health checks, secret handling and migration requirements.

## Current boundary

Identity, player accounts, the cache foundation and disabled M-Pesa collection
plumbing are implemented. Tournament draw/progression algorithms, organizer and
admin/referee dashboards, production evidence storage, continuous payment
reconciliation/refunds and the planned ranking/match handlers are not implemented.
See `docs/platform-readiness.md` before treating schema or demo UI as finished
behavior.
