# Production availability and scale architecture

## Target data plane

```text
React Native / marketing / organizer clients
                    |
        DNS -> CDN -> WAF -> L7 load balancer
                    |
       3+ stateless Go API pods across zones
          |          |                |
          |          |                +--> security Redis HA (no eviction)
          |          +-------------------> response-cache Redis HA (bounded, evictable)
          |
          +--> stable PostgreSQL writer endpoint
          |      managed multi-zone primary + automatic standby promotion
          |
          +--> stable PostgreSQL read endpoint
                 asynchronous replicas, replay-lag gated

Workers --> PostgreSQL outbox --> email / cache invalidation / reconciliation
Backups --> object storage + point-in-time recovery + tested restores
```

The local primary, replica and Redis containers prove connection routing and WAL
replication. They share one Docker host and are therefore neither high availability
nor a production failover system.

## PostgreSQL

- PostgreSQL is authoritative for identity, permissions, tournament state, results
  and money. Use a managed multi-zone writer with automatic promotion and stable
  endpoints. A read replica scales tolerant reads; it does not make the writer HA.
- Mutations, authorization, payment state, result decisions and read-after-write
  flows use the writer. Public catalog/projection reads may use a replica. Before a
  cache fill, Gamics measures replay lag and opens a short writer-fallback circuit if
  lag exceeds `DATABASE_MAX_REPLICA_LAG` or the reader fails.
- Writer fallback is bounded by `DATABASE_FALLBACK_MAX_CONCURRENCY`; a replica outage
  must not turn every request into an unlimited second query against the primary.
- Connection limits are a cluster budget, not a per-pod performance knob. Reserve
  connections for migrations, operations and workers, then keep
  `max API pods * per-pod pool maxima` below the remaining budget. Defaults are eight
  writer and sixteen reader connections per API process. Add PgBouncer for runtime
  traffic when measurements require it.
- Migration jobs connect directly to PostgreSQL because session advisory locks are
  incompatible with transaction-pooled PgBouncer. Production API replicas set
  `RUN_MIGRATIONS=false`. A one-shot job applies checksummed, backward-compatible
  expand/contract migrations before a rolling deployment.
- `000004_integrity_and_indexes` is a pre-launch integrity backfill, not an online
  migration for a large populated database. Apply it now, before Gamics accepts
  player traffic. If it ever has to run against a live populated system, split its
  backfills, validations and indexes into measured expand/validate/contract jobs
  (including concurrent index builds) before deployment.
- Connection lifetime jitter prevents every pod recycling connections together.
  Monitor pool acquire latency, utilization, query latency, lock waits, deadlocks,
  writer storage/IOPS and replica replay bytes/time.
- Enable encrypted backups and point-in-time recovery. Test restores quarterly and
  failover at least twice a year; an untested backup is not an availability plan.

PostgreSQL documents [hot-standby behavior](https://www.postgresql.org/docs/current/hot-standby.html)
and the [`pg_stat_replication` view](https://www.postgresql.org/docs/current/monitoring-stats.html#MONITORING-PG-STAT-REPLICATION-VIEW).

## API and Redis cache

Gamics uses cache-aside with PostgreSQL as the source of truth. The reusable response
cache stores exact JSON bytes, a weak content ETag and a soft freshness timestamp.
It supports fresh hits, stale-while-revalidate, local `singleflight`, a cross-pod
token-owned Redis rebuild lock, TTL jitter, bounded source loads and cache bypass.
Redis failure never changes business correctness.

Use two independent managed Redis services in production:

- `REDIS_SECURITY_URL`: email-code and registration limits and short-lived session state, reserved memory,
  TLS/auth and `noeviction`;
- `REDIS_CACHE_URL`: disposable public response bodies, a strict memory budget and a
  measured cache eviction policy such as `allkeys-lfu`.

Local Compose may point both URLs at one Redis. Do not copy that compromise to
production; cache eviction must never remove security controls.

Refresh tokens rotate with a five-minute retry grace for the previously presented
token, so a lost refresh response does not force another sign-in. Positive session
cache entries are capped at five seconds and logout returns an error unless Redis
revocation propagation succeeds; the writer database remains authoritative.

Initial policies:

| Representation | Fresh | Retained stale | Shared cache |
|---|---:|---:|---|
| Game catalog | 12 hours ±10% | 24 hours | Redis + CDN |
| Competition list/detail | 30 seconds | 5 minutes | after endpoint exists |
| Live bracket/standings | 2-5 seconds | 30 seconds | after engine exists |
| Completed bracket | 10 minutes | 24 hours | after engine exists |
| Safe public negative lookup | 5-10 seconds | none | after endpoint exists |

Never shared-cache authentication tokens, authorization/eligibility decisions,
payments, private player data, match rooms, score reports, evidence or result
reviews.
Mutation transactions write outbox events; an idempotent worker invalidates or bumps
the version of affected public projections only after commit. Cache fills after a
write must use the writer or replica-LSN awareness so a lagging replica cannot
repopulate just-invalidated data.

The implemented `GET /v1/games` also emits `ETag`, `Cache-Control` and `X-Cache`.
Conditional requests return `304`, reducing Redis and network traffic as well as
database load. Redis has official guidance for [atomic rate limiting](https://redis.io/docs/latest/commands/incr/),
[eviction](https://redis.io/docs/latest/develop/reference/eviction/) and
[Sentinel HA](https://redis.io/docs/latest/operate/oss_and_stack/management/sentinel/).

## Network and API processes

- Terminate public TLS at a managed CDN/WAF/load balancer and re-encrypt to private
  API ingress. PostgreSQL and Redis have private addresses only and require TLS.
- Run at least three stateless API replicas across failure zones with anti-affinity,
  autoscaling, rolling max-unavailable zero, a disruption budget and capacity for one
  zone or one replica to disappear.
- Load balancers use `/readyz`; process supervisors use `/healthz`. On shutdown the
  API becomes not-ready before its bounded graceful drain. Cache/replica degradation
  does not remove a healthy writer-backed pod; writer failure does.
- `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, request deadlines,
  a 32 KiB header cap and 64 KiB normal JSON body cap constrain slow clients. Daraja
  uses one pooled outbound transport with DNS/connect/TLS/header/whole-request
  deadlines and capped connections.
- Configure `TRUSTED_PROXY_CIDRS` to the actual ingress ranges. Forwarding headers are
  accepted only from those peers, preventing client-IP spoofing and global OTP lockout.
- Apply coarse rate limits at the edge and identity/phone/account-aware limits in the
  API. Compress public JSON with Brotli/gzip at the CDN. Restrict `/readyz` and
  operations endpoints to internal ingress where possible.

Go's [`http.Server`](https://pkg.go.dev/net/http#Server) and
[`http.Transport`](https://pkg.go.dev/net/http#Transport) document the timeout and
connection controls. NGINX documents common [HTTP load-balancing](https://nginx.org/en/docs/http/load_balancing.html)
behavior; use the equivalent controls in the chosen managed ingress.

## Availability and observability gates

Before a public launch, dashboards and alerts must cover:

1. API request rate, errors, duration, in-flight requests, saturation and deploy version.
2. PostgreSQL writer availability, pool waits, slow queries, locks, replication lag,
   backup age and restore-test results.
3. Redis hit/stale/miss/bypass rates, latency, errors, lock contention, memory,
   evictions and rejected writes.
4. OTP queue/delivery failures, abuse decisions and SMTP provider latency.
5. Daraja prompt/query/callback latency, unprocessed callback age, stale payment
   intents, reconciliation differences and refunds requiring action.
6. Outbox oldest-unprocessed age, attempts and dead-letter volume.

No architecture makes downtime impossible. This topology removes single API-process
failures, provides managed database/Redis failover, bounds dependency failures and
makes recovery measurable. The service still needs load tests, restore/failover
drills, capacity models and an explicit SLO before claiming production readiness.
