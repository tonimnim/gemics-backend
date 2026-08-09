# Gamics

Competition and player infrastructure for African esports, beginning with
eFootball Mobile in Kenya.

## What is scaffolded

- An Expo SDK 57 React Native player app for iOS and Android.
- A responsive marketing website using the Next.js App Router programming model.
- A Go API with graceful shutdown, timeouts, structured logs, CORS and request IDs.
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

The API starts on `http://localhost:8080`. PostgreSQL can be started with the
provided `compose.yaml` where Docker is available.

## Current boundary

This commit scaffolds the foundation; it does not yet implement accounts,
competition creation, bracket generation, uploads or payments. Those features
should be added vertically—contract, domain behavior, transaction, endpoint and
UI—rather than as disconnected horizontal layers.
