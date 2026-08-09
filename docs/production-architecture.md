# Production availability architecture

## Target topology

```text
Mobile / web clients
        |
CDN + WAF + load balancer
        |
3+ stateless Go API instances across availability zones
        |----------------------------|
stable PostgreSQL writer endpoint    PostgreSQL read endpoint
        |                            |-- read replica A
HA primary + synchronous standby     |-- read replica B (scale as measured)
        |
backups + point-in-time recovery

API instances ---> HA Redis endpoint ---> Redis primary/replicas or managed cluster
```

## Availability decisions

- A read replica scales reads; it does not make a database writer highly available.
  Production therefore uses a managed multi-zone PostgreSQL service or an operator
  with automatic promotion and a stable writer endpoint.
- Run at least three API instances with rolling deployments, readiness probes and
  disruption budgets. The processes are stateless; refresh sessions live in PostgreSQL.
- Redis accelerates OTP rate limiting and immediate access-token revocation. If Redis
  is unavailable, OTP limiting falls back to PostgreSQL and existing access tokens
  continue to validate, so Redis is not an authentication source of truth.
- Read replicas are asynchronous by default. Consistency-sensitive identity reads
  go to the writer, while tolerant discovery/list reads go to a replica with writer
  fallback. Track replica replay lag before adding more read traffic.
- Refresh sessions have no calendar expiry, matching the product requirement that a
  player remains signed in until logout. Tokens rotate on every refresh, are stored
  only as hashes server-side, and can be revoked per device. This increases the need
  for secure device storage, session management UI and incident-driven revocation.
- Schema migrations run once under an advisory lock and must remain backward-compatible
  during rolling deployments. Destructive cleanup happens only in a later release.

## Production safeguards

1. TLS everywhere, private database/Redis networks and secret-manager injection.
2. Automated PostgreSQL backups with point-in-time recovery and quarterly restore tests.
3. Automated failover exercises for the writer endpoint and Redis service.
4. Alerts for writer availability, replica lag, connection-pool saturation, OTP abuse,
   SMTP failure, refresh-token replay and elevated authorization errors.
5. Multi-zone API placement plus rolling/canary deployment health gates.

The local Compose primary/replica is for integration testing. Both containers still
run on one Docker host, so losing that host loses the whole local stack.
