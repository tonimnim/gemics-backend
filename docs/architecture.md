# Gamics architecture

## Decision summary

Gamics starts as a modular monolith: a portable web application, one Go API,
PostgreSQL as the transactional source of truth, and S3-compatible object
storage for match evidence. Background delivery uses a transactional outbox.

The first game adapter is `efootball-mobile`. The core model never uses an
eFootball-specific player or bracket table, so another solo or team title can
be added without rewriting registrations and matches.

```text
Player / Organizer browser
          |
          v
Next-compatible web UI  ----->  Go HTTP API
                                      |
                      +---------------+----------------+
                      |               |                |
                  PostgreSQL     Object storage    Outbox workers
                      |                                |
                      +------------> Analytics warehouse (later)
```

## Domain boundaries

The Go process is divided internally before it is divided operationally:

- **Identity:** users, sessions, age and country eligibility.
- **Organizations:** organizers, referees and role-based access.
- **Game catalog:** game-specific rules and optional publisher adapters.
- **Competitions:** lifecycle, formats, registration, check-in and seeding.
- **Matches:** scheduling, result confirmation, evidence, forfeits and disputes.
- **Ranking:** immutable rating inputs derived from confirmed matches.
- **Payments (later):** provider webhooks, refunds, payouts and a double-entry ledger.
- **Insights (later):** consented, de-identified events outside the operational database.

Packages communicate through explicit application interfaces and domain events.
They must not reach into another package's tables as a shortcut.

## eFootball result workflow

No public official Konami match-results API is assumed.

1. Gamics reveals both players and match instructions after check-in.
2. Players create an eFootball Friend Match and play 1v1.
3. One player submits the score and evidence object references.
4. The opponent confirms or disputes before a deadline.
5. Matching confirmation finalizes the result transactionally and advances the bracket.
6. Silence applies the published timeout rule; conflict creates a referee queue item.
7. Every decision appends an audit event. A correction supersedes, never overwrites, a submission.

The `GameResultProvider` boundary can later accept a signed Konami integration.
Scraping screens, automating player accounts or pretending unofficial data is
authoritative is deliberately excluded.

## Transaction and concurrency rules

- Mutating HTTP requests require an `Idempotency-Key` when retries could create duplicates.
- Bracket changes lock the competition or match row and check its version.
- Confirming a result, selecting a winner, advancing the next match and writing
  the outbox event occur in one database transaction.
- Workers deliver outbox events at least once. Consumers deduplicate on event ID.
- Money is always stored as integer minor units plus ISO currency; floats are prohibited.
- Uploaded evidence uses object keys and checksums. The database never stores video blobs.
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
for web, future mobile clients, workers and partner integrations. They belong in
Go. The web layer owns presentation and browser concerns only.

### Why not Redis as the queue and source of live state?

Losing a bracket transition or result event is unacceptable. PostgreSQL commits
business state and its outbox atomically. Redis can later accelerate rate limits,
ephemeral presence and caches, but is not authoritative.

### Why not model eFootball directly?

Tables such as `efootball_players` and `efootball_matches` would be quick for one
month and expensive forever. `game_accounts`, `competition_entries`, members and
versioned rules preserve eFootball-specific configuration while keeping the
competition engine reusable.

### Why not pool entry fees into prizes?

The initial schema distinguishes an administrative fee from organizer- or
sponsor-funded prizes. It intentionally has no player-funded prize-pool option.
That supports the sports-competition model and keeps the first release focused on free events.

### Why not ship a native app first?

Players already need eFootball on the same phone. A fast responsive web app can
handle registration, check-in, evidence capture and brackets without app-store
friction. Native clients become worthwhile when push delivery, media capture or
retention data proves the need.

## Extraction triggers

A module becomes a service only after at least one trigger is demonstrated:

- it needs independent scaling for a sustained workload;
- it needs a different availability or security boundary;
- its releases regularly block unrelated domains;
- a dedicated team owns it; or
- an external partner contract requires isolation.

Until then, a module remains independently testable inside the Go deployment.
