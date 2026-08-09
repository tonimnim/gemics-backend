# Gamics API

The API begins as a modular Go monolith. Domain packages do not depend on HTTP,
PostgreSQL, object storage, payment providers, or publisher-specific APIs.

Run locally from the repository root:

```sh
go run ./services/api/cmd/api
```

The initial routes are `GET /healthz`, `GET /readyz`, and `GET /v1/games`.
Business endpoints will be implemented against the contract in `openapi/openapi.yaml`.
