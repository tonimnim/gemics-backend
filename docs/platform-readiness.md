# Platform implementation readiness

This file prevents architecture diagrams and database tables from being mistaken for
finished product behavior. It reflects the codebase at this revision.

| Area | State | What exists | What is still required |
|---|---|---|---|
| Email authentication | Implemented | OTP request/verify, retry-safe rotating refresh sessions, propagated logout and onboarding | durable email queue/provider observability |
| Player/game accounts | Implemented | profile and eFootball account APIs | publisher-backed verification when available |
| Game catalog cache | Implemented | Redis SWR, ETag, stampede controls, replica lag circuit | metrics and production split Redis services |
| M-Pesa foundation | Implemented but disabled | STK Push/Query, callbacks, idempotency, rate limits, capacity reservation, atomic entry creation | real private credentials, sandbox certification, reconciliation worker and refund/admin operations |
| Competition formats | Schema only | enum values for single elimination, double elimination and round robin | draw generators, dependency graph, standings/tiebreakers and tests |
| Match progression | Schema only | match/result/dispute tables | result APIs, locking/version checks, advancement transaction and workers |
| Organizer dashboard | Not implemented | organization/RBAC tables | authorization middleware, organizer APIs and web UI |
| Admin/referee dashboard | Not implemented | roles, disputes and audit tables | queues, assignment/resolution APIs, payment review/refund tools and UI |
| Mobile player app | Prototype/demo | Native Expo shell, searchable virtualized rankings, public player profiles, match/result evidence and confirm/dispute demo flows | connect capability-gated ranking/match/evidence APIs after their Go handlers exist |
| Marketing site | Implemented | responsive public landing page | deployment analytics/SEO verification |

## Tournament engine findings

The strings `single_elimination`, `double_elimination` and `round_robin` currently
exist in Go constants and SQL constraints. They are not algorithms. There is no draw
generation, bye allocation, winner/loser routing, round-robin scheduler, standings,
tiebreak calculation or automatic advancement. The present `matches` table records a
round and match number but has no explicit dependency edges telling a match where its
home/away entrants come from.

A CueScore-like organizer experience needs these backend capabilities before its UI:

1. A deterministic, pure draw generator versioned by algorithm and seed. Given the
   same accepted entries, seeds and config it must produce the same graph.
2. Explicit match-slot sources such as direct entry, winner-of-match or loser-of-match.
   Progression follows stored graph edges; it must never infer the next match from
   round numbers.
3. One transaction that locks the decided match, checks its version, finalizes the
   accepted result, fills dependent slots, resolves byes, updates standings and writes
   audit/outbox events.
4. Idempotent commands and uniqueness constraints so two confirmations or referee
   actions cannot advance the same player twice.
5. Immutable draw/rules snapshots. Corrections supersede results and append audit
   events rather than rewriting history silently.

Format requirements:

- **Single elimination:** power-of-two bracket sizing, deterministic seeded placement,
  random/unseeded draw policy, byes, bronze-match option and best-of configuration.
- **Double elimination:** winner and loser brackets, defined loser-drop mapping,
  grand final and configurable bracket-reset rule. This must be generated from a
  tested template/graph, not ad-hoc arithmetic in HTTP handlers.
- **Round robin:** circle-method schedule for even/odd entrants, one bye per round for
  odd counts, home/away balancing, points policy and ordered tiebreak chain. Head-to-
  head mini-tables must specify how multi-way ties are handled.

Property tests should cover entrant counts from 2 through the supported maximum,
every entrant's expected match opportunities, no duplicate pairing within a round,
valid dependency edges, bye termination, and exactly one champion for terminal
knockout graphs. Concurrency integration tests must submit/confirm the same result in
parallel and prove one advancement.

## Dashboard boundary

Organization membership rows already support owner, admin, referee and analyst roles,
but no request authorization middleware uses them. A dashboard built directly against
the database would bypass tenant isolation and is not acceptable.

Build APIs and permission tests first:

- organizer competition CRUD/publish/registration/check-in/draw operations;
- entry lists, seeding, match operations and audit history;
- referee dispute queue, evidence review and superseding resolution;
- platform admin user/organization moderation and immutable audit views;
- payment-review, reconciliation and refund operations with separation of duties.

Then build a responsive Next.js organizer/admin application using those contracts.
The React Native app remains the player surface; the marketing landing page should not
become an authenticated operations console.

## Recommended delivery order

1. Finish organizer authorization and competition CRUD/registration contracts.
2. Add explicit match dependency schema and the single-elimination generator with
   property/concurrency tests.
3. Implement result submission, opponent confirmation/dispute and referee resolution;
   connect confirmed results to progression.
4. Add round-robin scheduling/standings, then double elimination.
5. Build organizer/referee UI against those stable APIs.
6. Add the M-Pesa reconciliation worker and payment-review/refund admin UI before
   enabling production collections.
7. Load-test cache hit/miss/failure modes, database failover, callback replay and a
   full tournament from registration through champion.
