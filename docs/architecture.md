# Gamics architecture

## Decision summary

Gamics starts with an Expo React Native player app, a separate marketing website,
one modular Go API, PostgreSQL as the transactional source of truth, and
S3-compatible object storage for match evidence. Background delivery uses a
transactional outbox.

The first game adapter is `efootball-mobile`. The core model never uses an
eFootball-specific player or bracket table, so another solo or team title can
be added without rewriting registrations and matches.

```text
React Native player app ----+
                            |
Organizer console (later) --+----> Go HTTP API
                            |            |
Marketing website ----------+    +-------+-------------------+
                                 |           |               |
                         PostgreSQL writer  PostgreSQL readers  Redis
                                 |                  |             |
                                 +--------+---------+-------------+
                                          |
                                  Object storage / outbox
                                          |
                                  Analytics warehouse (later)
```

## Domain boundaries

The Go process is divided internally before it is divided operationally:

- **Identity:** users, sessions, age and country eligibility.
- **Organizations:** organizers and role-based access (owner, admin, analyst). No
  organization role decides match results.
- **Game catalog:** game-specific rules and optional publisher adapters.
- **Competitions:** lifecycle, formats, registration, check-in and seeding.
- **Matches:** scheduling, check-in, blind score reports, screenshot evidence,
  forfeits, removal from the tournament and the Gamics result review queue.
- **Ranking:** immutable rating inputs derived from confirmed matches.
- **Payments:** Daraja payment intents, capacity reservations, callback/query
  verification and atomic paid registration. Refund automation, payouts and a
  double-entry ledger remain later work.
- **Insights (later):** consented, de-identified events outside the operational database.

Packages communicate through explicit application interfaces and domain events.
They must not reach into another package's tables as a shortcut.

## eFootball result workflow

No public official Konami match-results API is assumed.

1. Gamics reveals both players and match instructions after check-in.
2. Players create an eFootball Friend Match and play 1v1.
3. Each entry reports its score blind, without a screenshot and without seeing
   the other entry's claim. The first report starts the other entry's report
   window.
4. Equal reports finalize the result transactionally and advance the bracket.
5. Different reports open a response window in which each entry may send one
   final score with one to three screenshots. Claims that still differ after
   both responses go to the Gamics review queue, which only platform staff
   decide.
6. Silence past a window removes the silent entry from the tournament; nobody
   reporting by the result deadline removes both. Removals never change ratings.
7. Every decision appends an audit event. The confirmed score is written once,
   as the canonical result, and is never overwritten.

[Result verification](result-verification.md) describes the windows, the
review queue, strikes and who can see what.

The `GameResultProvider` boundary can later accept a signed Konami integration.
Scraping screens, automating player accounts or pretending unofficial data is
authoritative is deliberately excluded.

## Transaction and concurrency rules

- Mutating HTTP requests require an `Idempotency-Key` when retries could create duplicates.
- Bracket changes lock the competition or match row and check its version.
- Confirming a result, selecting a winner, removing silent entries, advancing
  the next match and writing the outbox event occur in one database
  transaction, after the competition's progression lock.
- Workers deliver outbox events at least once. Consumers deduplicate on event ID.
- Money is always stored as integer minor units plus ISO currency; floats are prohibited.
- Uploaded evidence uses object keys and checksums. Match evidence is JPEG or PNG
  screenshots only; the database never stores media blobs.
- Public identifiers are opaque UUIDs. Sequential audit IDs are never exposed as resource IDs.

## Scaling path

Scale by measured bottleneck rather than by an assumed player count:

1. **Launch:** one API deployment, one worker deployment, PostgreSQL, object storage and CDN.
2. **Read pressure:** cache public competition pages and bracket snapshots; add a read replica.
3. **Worker pressure:** split notifications, rankings and media processing into independent workers.
4. **Discovery pressure:** copy public player/competition documents into a search index.
5. **Analytics pressure:** stream consented outbox events into a columnar warehouse.
6. **Organizational pressure:** extract a domain service only when it has a distinct team,
   scaling profile or failure boundary. The existing package interface becomes its API.

This avoids distributed transactions during the stage when correctness and iteration speed matter most.

## Data products

The operational database is never sold or exposed to partners. A future insights
pipeline must apply consent, purpose limitation, retention, suppression and
de-identification before data reaches a partner-facing dataset.

- Aggregate reports may include tournament fill rate, retention, match duration
  and skill distributions with minimum cohort thresholds.
- Talent discovery is opt-in and separate from analytics consent.
- Children are excluded from commercial profiling and are private by default.
- Phone, email, government ID, payment data, raw chat, precise location and raw
  evidence are not analytics products.

## Decisions challenged

### Why not microservices now?

Tournament state crosses registration, matches, results and rankings. Splitting
those writes now would introduce distributed consistency and operational work
without a proven scaling benefit. Strong package boundaries retain the option to split later.

### Why not event sourcing?

An audit trail is essential, but reconstructing every bracket and organizer view
from events would slow development and complicate corrections. Relational current
state plus append-only audit events and an outbox provides the required history
without making every read a projection problem.

### Why not make the frontend the backend?

Competition rules, authorization and result decisions must behave identically
for mobile, future organizer clients, workers and partner integrations. They
belong in Go. Each client owns presentation and device-specific concerns only.

### Why not Redis as the queue and source of live state?

Losing a bracket transition or result event is unacceptable. PostgreSQL commits
business state and its outbox atomically. Redis accelerates rate limits, short-lived
session state and public response caches, but is not authoritative.

### Why not model eFootball directly?

Tables such as `efootball_players` and `efootball_matches` would be quick for one
month and expensive forever. `game_accounts`, `competition_entries`, members and
versioned rules preserve eFootball-specific configuration while keeping the
competition engine reusable.

### Why not pool entry fees into prizes?

The initial schema distinguishes an administrative fee from organizer- or
sponsor-funded prizes. It intentionally has no player-funded prize-pool option.
That supports the sports-competition model and keeps the first release focused on free events.

### Why ship the player experience in React Native now?

The player loop depends on check-in reminders, camera or gallery evidence,
deep links and eventually push notifications. Those are core to trustworthy
match operations, not optional polish. Expo React Native provides one iOS and
Android codebase while the lightweight website stays focused on discovery and
marketing. The trade-off is app-store release work and a second JavaScript
toolchain, which is contained under `apps/mobile` and kept behind the same Go API.

## Extraction triggers

A module becomes a service only after at least one trigger is demonstrated:

- it needs independent scaling for a sustained workload;
- it needs a different availability or security boundary;
- its releases regularly block unrelated domains;
- a dedicated team owns it; or
- an external partner contract requires isolation.

Until then, a module remains independently testable inside the Go deployment.

## Data-plane availability

The API accepts separate `DATABASE_WRITE_URL` and `DATABASE_READ_URL` values.
Mutations and consistency-sensitive identity reads use the writer. Safe catalog
and collection reads use the replica and fall back to the writer on connection
failure. Replication lag is expected, so mutation responses contain the newly
written resource instead of requiring an immediate replica read.

The Docker topology is a local parity environment: one PostgreSQL primary, one
asynchronous hot standby and Redis with AOF persistence. It demonstrates routing
and replication but is not itself a production control plane. Production uses a
stable managed writer endpoint with automated multi-zone failover, independently
scalable read replicas, multiple API instances behind a load balancer, and separate
high-availability security/cache Redis endpoints. See `production-architecture.md`
and `platform-readiness.md` for the implemented-versus-designed boundary.
