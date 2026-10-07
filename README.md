# Gamics

Competition and player infrastructure for African esports, beginning with
eFootball Mobile in Kenya.

## What is scaffolded

- An Expo SDK 57 React Native player app for iOS and Android.
- A responsive marketing website using the Next.js App Router programming model.
- A Go API with graceful shutdown, timeouts, structured logs, CORS and request IDs.
- Registration with only a username, Konami ID and password; Konami ID sign-in;
  email and phone added after registration; rotating refresh sessions,
  player/game-account APIs and a replica-aware Redis-cached game catalog.
- A role-based organizer API: an explicit permission matrix, per-route
  authorization over organization membership, and competition create/edit/publish
  with status-aware edit rules, guarded lifecycle transitions and audit history.
- A production-disabled Daraja foundation with STK Push/Query, durable callbacks,
  payment idempotency and atomic paid-entry creation.
- Deterministic draws and progression for single elimination, double elimination
  and round robin, with standings and placements.
- Blind result verification: each entry reports its score without seeing the
  other's, silent entries are removed from the tournament, and scores that still
  differ after a screenshot-backed response go to a Gamics staff review queue.
  Organizers never decide results.
- A PostgreSQL schema for organizers, solo/team entries, brackets, score reports,
  screenshot evidence, result reviews, conduct strikes, idempotency, audit history
  and transactional outbox delivery.
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
docs/result-verification.md  Blind score reports, removal and Gamics review policy
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

Identity, player accounts, the cache foundation, the organizer authorization and
competition-management API, draws and progression, blind result verification with
the Gamics review queue, and disabled M-Pesa collection plumbing are implemented.
The organizer and Gamics staff web UI, production evidence storage and continuous
payment reconciliation/refunds are not.

A competition reaches `running` only after its draw is generated; otherwise the
transition is refused with `bracket_not_generated` rather than starting an event
with no matches. See `docs/platform-readiness.md` before treating schema or demo
UI as finished behavior.
