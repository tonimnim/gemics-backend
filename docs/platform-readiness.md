# Platform implementation readiness

Last audited: 2026-09-28. The machine-readable source of truth is
`services/api/openapi/openapi.yaml`; see `backend-api-status.md` for deployment
dependencies and `mobile-api-requirements.md` for the player journey.

| Area | State | Production boundary |
|---|---|---|
| Email authentication and sessions | Implemented | Configure SMTP; add provider observability and bounce/complaint handling |
| Player identity, legal consent, avatar and account deletion | Implemented | Use private production object storage and a secure staff bootstrap |
| Game accounts and evidence verification | Implemented | Manual review is not publisher verification; add a publisher integration only if Konami offers one |
| Discovery, search, rankings and histories | Implemented | Load-test snapshot projection, trigram search and cache hit/miss behavior |
| Competition eligibility and registration | Implemented | Final policy and capacity checks remain writer-transaction authoritative |
| M-Pesa paid entry | Implemented but provider-disabled by default | Configure approved Daraja credentials/HTTPS callback and independent lost-receipt reconciliation |
| Payment/refund/player history and admin review | Implemented | Connect a B2C/refund worker; a durable request is not reported as paid until a provider receipt exists |
| Blind dual score reports, mismatch responses and screenshot evidence | Implemented | Run the DB-gated result verification tests against PostgreSQL; screenshots are JPEG/PNG only |
| Result deadlines and removal from the tournament | Implemented | The verification worker settles report, response and result deadlines; watch removals and evidence error rates after launch |
| Gamics result review queue, strikes and registration ban | Implemented | Build the staff review console against OpenAPI; bootstrap reviewer accounts securely |
| Single/double elimination and round robin | Implemented | Add historical correction/unwind and richer head-to-head tie-break policies if required |
| Draw persistence and progression | Implemented | Run concurrency/load tests against live PostgreSQL before public paid events |
| No-show handling | Implemented for ready matches | In-progress matches with no report by the result deadline remove both entries; no result is ever guessed |
| Organization authorization and competition operations | Implemented | Build the organizer UI; keep every action permission-scoped server-side |
| Notifications and Expo push receipts | Implemented | Configure Expo, monitor delivery jobs, and add transactional notification email if enabled |
| PostgreSQL writer/reader and Redis cache/security split | Implemented in service configuration | Use managed failover/replicas and separate production Redis policies; Compose is not HA |
| Local private media storage | Implemented with MinIO | Use HTTPS, scoped credentials and managed S3-compatible storage in production |
| Mobile player app | API-ready; client integration remains in its own repository | Generate types from OpenAPI 0.9.0 and disable fixtures only when the local stack is reachable |
| Organizer and Gamics staff UI | Not implemented in this backend repository | Build dedicated authenticated clients; do not turn the marketing page into an operations console |
| Marketing site | Implemented | Deploy separately from the mobile app and verify analytics/SEO |

## Tournament engine guarantees

- Draw generation is deterministic for the same frozen rules, eligible-entry vector,
  seeding policy and algorithm version; draw provenance is persisted.
- Match slots store direct-entry, winner-of and loser-of dependencies. Progression
  follows the persisted graph instead of inferring edges from round numbers.
- Result confirmation, removal of silent entries, review decisions, rating changes,
  dependent slots, round-robin standings, placements, audit and outbox events share
  one transaction behind the competition's progression lock.
- Removed entries never advance, never drop into a losers bracket and receive no
  placement; their remaining round-robin fixtures are settled as walkovers.
- A source match/version/outcome ledger makes progression idempotent and rejects a
  conflicting replay.
- Single elimination supports seeded placement, byes and bronze configuration.
  Double elimination supports winner/loser routing and conditional grand-final reset.
  Round robin uses a persisted schedule, points/goal-difference/goals ordering and
  sequential-round release.
- Ready-match deadlines are polled with indexed, leased `SKIP LOCKED` work. One
  checked-in side wins by forfeit; zero check-ins cancel the match; two check-ins are
  never auto-decided.

## Authorization and operational boundary

Organization role checks use the PostgreSQL writer to avoid granting revoked access
during replica lag. Tenant IDs are also constrained in handler SQL, and non-members
receive `404` to avoid enumeration. Platform payment, refund, game-verification and
result-review permissions are separate from organization roles, and no organization
role decides a match result.

Production migrations run once through an external migration job. API replicas set
`RUN_MIGRATIONS=false` and roll behind a load balancer only after the migration job
succeeds. Migration `000016` installs the notification outbox trigger and performs a
gap-free historical backfill; schedule that migration during a controlled window
because it briefly blocks outbox writers in proportion to existing outbox size.

## Before public paid competitions

1. Execute all migrations against a PostgreSQL staging copy and test rollback policy.
2. Run a full tournament for each format, including byes, no-shows, mismatched
   reports, removals, Gamics review decisions, late callbacks, refunds and duplicate
   requests.
3. Load-test ranking/search/competition caches, Redis failure, replica lag and writer
   fallback limits.
4. Verify Daraja sandbox callbacks, STK Query, independent transaction recovery and
   the real refund provider workflow.
5. Exercise backup restore, database failover, rolling API deployment, push receipts,
   private-media access revocation and staff audit trails.
