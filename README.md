# Gamics

Competition and player infrastructure for African esports, beginning with
eFootball Mobile in Kenya.

## What is scaffolded

- A responsive landing website using the Next.js App Router programming model.
- A React staff dashboard (`apps/admin`). The player app is built in Flutter in its
  own repository, against the API and contract in this one.
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
- Result verification: either entry submits the result and the other confirms or
  rejects it; an unanswered result stands, and a rejected one goes to a staff review
  queue with one screenshot from each player, read by the screenshot reader.
  Organizers never decide results.
- A PostgreSQL schema for organizers, solo/team entries, brackets, score reports,
  screenshot evidence, result reviews, conduct strikes, idempotency, audit history
  and transactional outbox delivery.
- An OpenAPI contract and an architecture rationale.

## Repository map

```text
app/                         Landing website
apps/admin/                  Staff dashboard
services/api/cmd/api/        Go API entry point
services/api/internal/       Domain and transport modules
services/api/migrations/     PostgreSQL schema
services/api/openapi/        HTTP contract
docs/architecture.md         Boundaries, scale path and challenged decisions
docs/docker.md               Containers, API addresses and deployment notes
docs/result-verification.md  Submit, confirm or reject, screenshots and review policy
docs/mobile-api-requirements.md  API boundary for the Flutter player app
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

In another terminal, start the API:

```sh
npm run api:dev
```

Checks:

```sh
npm run build
npm run lint
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
