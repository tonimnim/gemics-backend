# Bracket engine and ranking: the decision

Produced by a 13-agent design review: four independent proposals, eight adversarial
judges across a correctness lens and an operability lens, then one synthesis. Claims
about existing code were checked against the source.

Status: **decided, not implemented.** None of the bracket or ranking code below exists
yet. See platform-readiness.md for what is actually built.

> **Superseded in part (2026-09-28).** Result verification v2 retired organizer
> result decisions, the single-submission confirm flow and everything built around
> them: results now come from blind dual score reports, and contested matches go
> to the Gamics review queue ([result verification](result-verification.md)).
> Passages marked *[Superseded by result verification v2]* describe the retired flow and are kept as
> design history only. `referee` survives only as a legacy value in the
> `completion_reason` and progression `cause` CHECKs; nothing writes it.

---
# Bracket Engine + Rating: The Decision

Verified against source before writing: `result_handlers.go:113` (`RatingChanges`), `:116` (`ratingChangeView`), the `UPDATE matches ... WHERE id=$2 AND version=$3 AND state='awaiting_confirmation'` CAS, `applyConfirmedResultRatings` (K=32, `math.Pow`), `lockSubmissionForDecision` ending in `FOR UPDATE OF submission,match` with INNER JOINs on **both** `competition_entries`, and `organizer_routes.go` — which registers organization, member, competition, transition and entry routes and **zero** match, draw, or bracket routes.

Two corrections to the brief, both load-bearing:

1. **A rating algorithm already exists and is already public.** `applyConfirmedResultRatings` writes `player_game_ratings` today, `resultDecisionView.RatingChanges` returns the exact before/after/delta to the player, `writeResultAuditAndOutbox` copies it into `outbox_events.payload`, and `publish_leaderboard_snapshot` sorts the public board by it. This is a *replacement under live data plus an OpenAPI break*, not a greenfield add.
2. **Byes structurally cannot reach the confirmation path.** `lockSubmissionForDecision` INNER JOINs both entry rows, so a NULL-side match can never produce a decidable submission. Any design whose bye handling hangs off that function is dead code.

---

## Decision 1 — Tournament structure: **Materialized Bracket Graph (MBG)**

**MBG wins.** Judges: 7 / 7 vs 6.5 / 6 for Format Plugin Per Stage.

### Why Format Plugin lost

Not on architecture — its edge table (`match_slot_sources`) is the same correct idea. It lost on **three mechanisms that do not work as written** plus a premise it cannot reach:

- **The grand-final reset cannot commit.** Step (C) reads each target's `version` once under `FOR UPDATE`; step (D) applies each edge with `WHERE id=$tm AND version=$v` asserting `RowsAffected()==1`, while each success does `version=version+1`. The reset is deliberately the one target fed by two edges from one source. When the LB champion wins, the first edge bumps to `$v+1`, the second still predicates on `$v`, matches zero rows, and aborts the transaction. **Every double-elimination event that needs a reset becomes an unconfirmable 409 loop at its most visible match.**
- **Silent double-advance through `stage_rank` edges.** `match_slot_sources_stage_rank_uidx` indexes `(source_stage_id, source_scope, source_group_key, source_rank)` while the row CHECK *requires* `source_group_key IS NULL` for `cross_group`. Postgres unique indexes are NULLS DISTINCT, so `(S0,'cross_group',NULL,1)` inserts twice and two knockout slots both claim best-third-place #1. None of its three advertised anti-double-advance mechanisms catch it — in exactly the group-into-knockout shape the proposal spends a section arguing is the right product for Kenya.
- **`rating_applications.UNIQUE(match_id)` forbids the correction flow the same document calls mandatory.** Its own schema comment concedes the referee "must first release the UNIQUE(match_id) claim" — delete the row the reversal is supposed to reference. Self-contradictory, and it destroys the audit trail the table exists for. *[Superseded by result verification v2]* The organizer role it names no longer exists; the correction argument still holds.
- **The whole stage-driver premise has no authoring path.** `competition_stages` is read-only across the entire codebase; `organizer_routes.go` exposes no stage route. Its flagship 8-groups-of-4 composition must be hand-authored into `config` jsonb. Not costed in its 8 weeks.

MBG's flaws are real but **local**: a non-void-aware fill path, an invalid deadlock proof for multi-level cascades, an unlocked stage-finalization probe, a seed collision, an incomplete replay story. Every one has a contained fix that does not touch the model. Format Plugin's break the model at the seam it calls hardest — and its own section 9(8) admits "one interface" is really seven methods and growing.

### What I grafted from Format Plugin

| Graft | Fixes |
|---|---|
| **`pg_advisory_xact_lock(hashtextextended('competition:'\|\|id, 91340287))` taken as the FIRST lock, before `lockSubmissionForDecision`** | MBG's deadlock-freedom proof was invalid for multi-level void cascades (locks taken 5, 9, then 6). The gate makes cross-transaction ordering irrelevant *within* a competition, which is stronger and cheaper than MBG's proposed two-phase recursive-CTE lock closure. Its own precedent exists at migration `000007:111`. |
| **Stage `FOR UPDATE` as its own statement before the completeness count** | MBG's stage finalization was an unlocked `NOT EXISTS` probe — when the last two matches confirm concurrently both snapshots see the other open, *neither* finalizes, and placements are silently never written. Under READ COMMITTED the second statement takes a fresh snapshot, so exactly one wins and it is always the last committer. |
| **`progression_events` append-only ledger** | Staff can answer "why is this player in this match" without replaying the event. |
| **`completion_reason` on `matches`** | Distinguishes played / walkover / double no-show / referee / timeout / reset-not-required inside one `state` value. *[Superseded by result verification v2]* `referee` is now a legacy value; v2 adds `report_timeout`, `response_timeout`, `no_result_reported`, `platform_review` and `competition_cancelled`. |
| **Round-robin groups as `matches.bracket = 'group_a'..`** | Reuses the existing `UNIQUE(stage_id, bracket, round_number, match_number)` with zero schema change, and lifts MBG's hard 20-entry round-robin cap: 8 groups of 4 is 48 fixtures, not 523,776. |
| **Recursive head-to-head tiebreak chain terminating in `sha256(competition_id \|\| entry_id)`** | MBG's tiebreak was hand-waved. This one is a provable total order, so recomputation is deterministic. |
| **"A caller may decide *whether* a stored edge fires, never *where* it goes"** | Best one-line formulation of the architecture ban on round arithmetic, because it makes the ban testable: the applier contains no arithmetic to audit. |

**Rejected from Format Plugin:** the per-stage `Driver` interface. Three emitters plus one shared applier is the right factoring; a plugin boundary that admits it cannot express withdrawal, void, or disqualification is a boundary in the wrong place.

### MBG bugs I am fixing before implementation

1. **Void-aware settle.** Merge phases 5(b) and 5(c) into one `settleChild(childID)` that re-reads both slots under the held child lock and derives state as a pure function of the two `(resolved_entry_id, voided_at)` pairs. As specified, a parent that resolves with no winner *before* its sibling leaves the child permanently pending — one participant, one voided slot, never ready, never forfeit, branch dead forever.
2. **Stop reusing `competition_entries.seed` as the draw-position record.** `competition_entries_seed_uidx` is `UNIQUE(competition_id, seed) WHERE seed IS NOT NULL` and seeds are arbitrary positive integers, not contiguous. Ten entries, one pre-seeded as `seed=5`: draw position 5 collides, 23505, whole draw rolls back, competition stuck in `check_in`. New table `competition_draw_entries` instead — which also supplies the stored entry vector replay needs.
3. **Kill the `COALESCE` schedule anchoring.** First slot fill wins, so a final that becomes ready at 15:30 carries a check-in window that opened and closed at 14:00. Compute all four timestamps unconditionally at the moment the child flips to `ready`.
4. **Entry liveness in `settleChild`.** Treat `competition_entries.status IN ('withdrawn','disqualified')` as equivalent to a voided slot. DQ then needs zero DQ-specific progression code, and in double elimination it correctly settles both bracket branches from one action.
5. **Round-robin round gating.** Insert only round 1 as `ready`; later rounds `pending` with populated windows, released by the deadline worker or `POST .../rounds/{n}/release`.
6. **Staff write path.** Three organizer routes, because a bracket only players can advance is unusable for a first-party organizer.

---

## Decision 2 — Rating: **Two-Layer Ranking** (public points ladder + hidden integer Elo)

**Two-Layer wins.** Judges: 7 / 4 (avg 5.5) vs 6 / 4 (avg 5.0) for Tournament-Weighted Elo. Closer than the bracket call, and I am choosing partly on composition with MBG.

### Why TWE lost

- **It is inert on delivery and depends on us.** `matches.rating_weight_milli` is written only by the draw generator. Until MBG lands, every match takes DEFAULT 1000 and all three formats rate flat — every format-specific stake decision in its `formatCoverage` is switched off. Five weeks replacing a *working* K=32 Elo while the actual blocker is the draw generator is the wrong sequencing.
- **Corrections and reversals have no idempotency guard at all.** `rating_events_one_per_player_match_uidx` is scoped `WHERE kind='match'`. Nothing constrains `correction` or `reversal`. A retried ban wave double-applies — in exactly the at-least-once outbox scenario the design uses to justify that index existing.
- **Its own stated antisymmetry invariant is false.** "E(d) + E(-d) == 1000 EXACTLY" holds for the *table* but not the *function*: `index = (d+1600)/4` floors, so `expect(1)+expect(-1) = 999`. Its property test tests the table, so it passes while the invariant is violated for every rating difference not a multiple of 4.
- **The rating floor is a point source.** Existing code clamps with `max(0, ...)`, so stored ratings can be in `[0,100)`, and its `seed_import` copies them verbatim. Its floor rule then awards a player at rating 40 **+60 for losing**.
- **Two unbounded critical-path queries** (per-competition gain cap, distinct-opponent EXISTS) carry no time predicate and no matching index, so on a RANGE-partitioned ledger they scan every retained partition — twice per confirmation, while holding `FOR UPDATE` on the live match row. The design names this as its own top risk and then designs it in.
- **Publishing a tournament-weighted Elo makes it uninterpretable.** K is a learning rate, not an importance weight. Multiplying it by tier and stake makes the public number part skill-estimate, part achievement score, systematically miscalibrated for anyone whose match mix skews. TWE's own risk list says this.

### Why Two-Layer is the right product for this market

Points attach to **placement**, awarded once per competition. A bracket pays at most once no matter how many times you beat the same person inside it. The player-facing rule is one sentence and the arithmetic is addition: *"Nairobi Open, 64 players, finished 3rd: 50 × 5 × 1 = 250."* In a market where identity is weak (one SIM + one M-Pesa entry fee buys a smurf) and no-shows are constant, an explainable placement ladder is defensible in a way a probabilistic per-match number is not. Elo stays — hidden — for the one job that genuinely needs a probabilistic number: seeding.

It also composes with MBG cleanly. The ladder consumes `competition_entry_placements`, which MBG's `finalizeStage` writes. The hidden Elo feeds MBG's draw. Neither is inert.

### What I grafted from TWE

| Graft | Why |
|---|---|
| **`rating_deviation` + LCB seeding: `seed_key = max(rating − RD, peak_rating − 150)`** | An unknown seeds at 1150 and meets a top seed in round one — anti-smurf seeding falls out of the uncertainty term. `peak_rating − 150` means deliberate tanking still seeds near peak. Two-Layer had neither; this is the single best idea in TWE. |
| **Frozen seed snapshot (`competition_draw_entries` carries `rating_snapshot`, `deviation_snapshot`)** | Merges TWE's `competition_seed_inputs` into MBG's replay table. One table serves draw replay and seeding audit. |
| **Legacy floor clamp in the migration (`GREATEST(rating,100)`)** | Closes the sub-100 point source before any new rule can exploit it. |
| **`factors`/`algorithm_version` on every ledger row + `REVOKE UPDATE, DELETE`** | Makes append-only a database fact rather than a convention. |
| **Graduation gate (`rated_matches >= 10 AND distinct_opponents >= 6`)** | Used for `provisional` → seeding confidence, not for a public tier. |
| **Leaderboard scaling fixes (cap rank ≤ 10 000, on-demand self-rank via index-only count, 15-min publish, retain-newest)** | Both proposals found the same live landmine: `publish_leaderboard_snapshot` writes ~3.6M rows/hour forever with no retention. |

### Two-Layer bugs I am fixing before implementation

1. **Retraction is impossible as specified** — `ladder_awards CHECK (points_awarded >= 0)`, PK `(competition_id, entry_id)` with `ON CONFLICT DO NOTHING`, and increment-only standings, yet the stated correction policy is "recompute the whole competition." Fix: `superseded_at` + a partial unique index on live rows, and standings **recomputed** as a SUM over live awards for the affected users under the season advisory lock. The headline invariant survives corrections instead of being falsified by the first one.
2. **`player_rating_events` PK `(match_id, user_id)` blocks the compensating row the design prescribes.** Fix: `id uuid PRIMARY KEY`, with the rate-once guarantee re-expressed as a partial unique index — and keyed on **`submission_id`, not `match_id`**, so a superseding submission can rate as a new event while the reversal stays in the ledger. "Corrections supersede, never overwrite" then holds for ratings too.
3. **`ladder_pair_meetings` counter is never reversed on void/correction/DQ.** Fix: delete the table; derive meetings as a COUNT over non-reversed ledger rows for the canonical pair. One indexed count per confirmation, and void/correction/DQ become automatically consistent. This also removes the fastest-growing table in the design.
4. **The season-scoped pair gate misfires on legitimate elite play.** `pairCountsPerSeason=2` means a DE pair meeting in winners and again in the grand final burns its entire season budget in one event; the scene's top two then score **zero** in later small events. Fix: rolling 30-day window, threshold 6, and repeat-meeting exclusion applies **only to hidden-rating damping**, never to ladder `qualifying_matches`.
5. **Field size must count *checked-in* entries, not entries that played.** As written, other players' no-shows shrink your multiplier and change your payout for an identical finish — which destroys the "explain your own rank" premise in the one market where no-shows are constant. Check-in is rate-limited and identity-bound, so the bot-field defence survives.
6. **`disqualified` added to the eligibility enum** and read from `competition_entries.status`. Today a DQ'd cheater holding a 2nd-place row is awarded full points with `eligibility='scored'`.

---

## Migration 000009 — real DDL

```sql
-- 000009_bracket_graph_and_ranking.up.sql
-- Expand-only except for the deliberate pre-launch snapshot reset at the end.
BEGIN;

-- =====================================================================
-- 1. matches: graph metadata, composite FK targets, rate-once latch
-- =====================================================================
ALTER TABLE matches
    ADD COLUMN graph_rank integer NOT NULL DEFAULT 1,
    ADD COLUMN group_key text,
    ADD COLUMN activation_rule text NOT NULL DEFAULT 'unconditional'
        CHECK (activation_rule IN ('unconditional','if_source_away_wins')),
    ADD COLUMN activation_source_match_id uuid,
    ADD COLUMN completion_reason text CHECK (completion_reason IN
        ('played','walkover','double_no_show','referee','timeout_forfeit', -- superseded: 'referee' is legacy (result verification v2)
         'reset_not_required','correction_voided')),
    ADD COLUMN rated_submission_id uuid REFERENCES result_submissions(id),
    ADD CONSTRAINT matches_graph_rank_positive_chk CHECK (graph_rank > 0),
    ADD CONSTRAINT matches_activation_pair_chk CHECK (
        (activation_rule =  'unconditional' AND activation_source_match_id IS NULL) OR
        (activation_rule <> 'unconditional' AND activation_source_match_id IS NOT NULL)),
    ADD CONSTRAINT matches_rated_requires_completion_chk
        CHECK (rated_submission_id IS NULL OR completed_at IS NOT NULL);
ALTER TABLE matches ALTER COLUMN graph_rank DROP DEFAULT;   -- table is empty today
ALTER TABLE matches SET (fillfactor = 85);                  -- HOT room: 3-4 UPDATEs per lifecycle

-- matches is the only core table lacking the composite targets 000004 gave
-- competition_stages and competition_entries. Every FK below needs them.
CREATE UNIQUE INDEX matches_id_competition_uidx ON matches (id, competition_id);
CREATE UNIQUE INDEX matches_id_rank_uidx        ON matches (id, graph_rank);

CREATE INDEX matches_stage_open_idx  ON matches (stage_id)
    WHERE state NOT IN ('completed','forfeit','cancelled');
CREATE INDEX matches_stage_graph_idx ON matches (stage_id, bracket, graph_rank, match_number);
CREATE INDEX matches_deadline_idx    ON matches (result_due_at)
    WHERE state IN ('ready','in_progress') AND result_due_at IS NOT NULL;
CREATE INDEX matches_release_idx     ON matches (stage_id, round_number)
    WHERE state = 'pending' AND check_in_opens_at IS NOT NULL;

-- =====================================================================
-- 2. match_slots: THE dependency edges. matches cannot express provenance.
--    source_* is write-once at draw time; resolved_*/voided_at are the only
--    mutable columns. Covers all three formats:
--      single_elimination : winner_of  (+ 2 loser_of edges for bronze)
--      double_elimination : winner_of + loser_of + one activation rule
--      round_robin        : every slot is 'entry' -> zero edges
-- =====================================================================
CREATE TABLE match_slots (
    match_id          uuid    NOT NULL,
    competition_id    uuid    NOT NULL,
    slot              text    NOT NULL CHECK (slot IN ('home','away')),
    match_rank        integer NOT NULL CHECK (match_rank > 0),
    source_kind       text    NOT NULL CHECK (source_kind IN ('entry','winner_of','loser_of')),
    source_entry_id   uuid,
    source_match_id   uuid,
    source_rank       integer,
    resolved_entry_id uuid,
    resolved_at       timestamptz,
    voided_at         timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (match_id, slot),
    FOREIGN KEY (match_id, competition_id)        REFERENCES matches (id, competition_id) ON DELETE CASCADE,
    FOREIGN KEY (match_id, match_rank)            REFERENCES matches (id, graph_rank),
    FOREIGN KEY (source_match_id, competition_id) REFERENCES matches (id, competition_id),
    FOREIGN KEY (source_match_id, source_rank)    REFERENCES matches (id, graph_rank),
    FOREIGN KEY (source_entry_id, competition_id) REFERENCES competition_entries (id, competition_id),
    FOREIGN KEY (resolved_entry_id, competition_id) REFERENCES competition_entries (id, competition_id),
    CONSTRAINT match_slots_shape_chk CHECK (
        (source_kind =  'entry'
             AND source_entry_id IS NOT NULL AND source_match_id IS NULL AND source_rank IS NULL) OR
        (source_kind IN ('winner_of','loser_of')
             AND source_entry_id IS NULL AND source_match_id IS NOT NULL AND source_rank IS NOT NULL)),
    -- Acyclicity is a DDL fact, not a generator convention.
    CONSTRAINT match_slots_acyclic_chk  CHECK (source_rank IS NULL OR source_rank < match_rank),
    CONSTRAINT match_slots_not_self_chk CHECK (source_match_id IS NULL OR source_match_id <> match_id),
    CONSTRAINT match_slots_resolution_chk CHECK ((resolved_entry_id IS NULL) = (resolved_at IS NULL)),
    CONSTRAINT match_slots_exclusive_chk  CHECK (NOT (resolved_at IS NOT NULL AND voided_at IS NOT NULL))
);

-- A match's winner feeds at most one slot; so does its loser. Keying on
-- source_kind (not source_match_id alone) admits the two legitimate
-- same-source cases: the DE grand final at N=2, and the bracket reset.
CREATE UNIQUE INDEX match_slots_one_consumer_uidx
    ON match_slots (source_match_id, source_kind) WHERE source_match_id IS NOT NULL;
CREATE INDEX match_slots_open_source_idx
    ON match_slots (source_match_id) WHERE resolved_at IS NULL AND voided_at IS NULL;

-- =====================================================================
-- 3. Draw provenance + the stored ordered entry vector (replay depends on it)
-- =====================================================================
CREATE TABLE competition_draws (
    competition_id    uuid PRIMARY KEY REFERENCES competitions(id) ON DELETE CASCADE,
    stage_id          uuid    NOT NULL,
    algorithm         text    NOT NULL,
    algorithm_version integer NOT NULL CHECK (algorithm_version > 0),
    draw_seed         bytea   NOT NULL CHECK (octet_length(draw_seed) = 32),
    seeding_policy    text    NOT NULL CHECK (seeding_policy IN ('seeded','rating','random','registration_order')),
    entry_count       integer NOT NULL CHECK (entry_count >= 2),
    entry_fingerprint bytea   NOT NULL CHECK (octet_length(entry_fingerprint) = 32),
    graph_fingerprint bytea   NOT NULL CHECK (octet_length(graph_fingerprint) = 32),
    match_count       integer NOT NULL CHECK (match_count > 0),
    config            jsonb   NOT NULL DEFAULT '{}'::jsonb,
    generated_by      uuid    NOT NULL REFERENCES users(id),
    generated_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (stage_id, competition_id) REFERENCES competition_stages (id, competition_id)
);
-- PRIMARY KEY(competition_id) is the exactly-once guard: two concurrent draw
-- requests cannot both succeed even without the competitions FOR UPDATE.

CREATE TABLE competition_draw_entries (
    competition_id     uuid    NOT NULL REFERENCES competition_draws(competition_id) ON DELETE CASCADE,
    draw_position      integer NOT NULL CHECK (draw_position > 0),
    entry_id           uuid    NOT NULL,
    group_key          text,
    seed_key           integer NOT NULL,
    rating_snapshot    integer,
    deviation_snapshot integer,
    PRIMARY KEY (competition_id, draw_position),
    UNIQUE (competition_id, entry_id),
    FOREIGN KEY (entry_id, competition_id) REFERENCES competition_entries (id, competition_id)
);
-- Replaces "UPDATE competition_entries SET seed = <draw position>", which
-- collides with competition_entries_seed_uidx on any sparse organizer seeding,
-- and supplies the frozen input vector that makes graph_fingerprint replay real.

-- =====================================================================
-- 4. Round-robin / group standings (rows pre-created at draw time)
-- =====================================================================
CREATE TABLE competition_standings (
    stage_id       uuid NOT NULL,
    competition_id uuid NOT NULL,
    entry_id       uuid NOT NULL,
    group_key      text NOT NULL DEFAULT 'main',
    played    integer NOT NULL DEFAULT 0 CHECK (played    >= 0),
    wins      integer NOT NULL DEFAULT 0 CHECK (wins      >= 0),
    draws     integer NOT NULL DEFAULT 0 CHECK (draws     >= 0),
    losses    integer NOT NULL DEFAULT 0 CHECK (losses    >= 0),
    walkovers integer NOT NULL DEFAULT 0 CHECK (walkovers >= 0),
    goals_for     integer NOT NULL DEFAULT 0 CHECK (goals_for     >= 0),
    goals_against integer NOT NULL DEFAULT 0 CHECK (goals_against >= 0),
    points integer NOT NULL DEFAULT 0,
    standings_version bigint NOT NULL DEFAULT 0 CHECK (standings_version >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (stage_id, entry_id),
    FOREIGN KEY (stage_id, competition_id) REFERENCES competition_stages (id, competition_id),
    FOREIGN KEY (entry_id, competition_id) REFERENCES competition_entries (id, competition_id),
    CHECK (played = wins + draws + losses)
);
CREATE INDEX competition_standings_table_idx ON competition_standings
    (stage_id, group_key, points DESC, (goals_for - goals_against) DESC, goals_for DESC, entry_id);

-- =====================================================================
-- 5. Progression audit: why is this player in this match
-- =====================================================================
CREATE TABLE progression_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    competition_id  uuid NOT NULL REFERENCES competitions(id) ON DELETE CASCADE,
    stage_id        uuid NOT NULL,
    source_match_id uuid,
    target_match_id uuid,
    target_slot     text CHECK (target_slot IN ('home','away')),
    entry_id        uuid,
    kind text NOT NULL CHECK (kind IN
        ('slot_filled','slot_voided','match_readied','match_forfeited','match_cancelled',
         'round_released','stage_completed','placements_written','subgraph_unwound')),
    cause text NOT NULL CHECK (cause IN
        ('draw','player_confirmation','referee','timeout_forfeit','withdrawal', -- superseded: 'referee' is legacy; v2 adds 'platform_review'
         'disqualification','admin_correction')),
    actor_user_id uuid REFERENCES users(id),
    detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX progression_events_competition_idx ON progression_events (competition_id, id DESC);

-- =====================================================================
-- 6. Hidden rating: exact integers, private, seeding-only
-- =====================================================================
ALTER TABLE player_game_ratings
    ADD COLUMN rating_milli       integer NOT NULL DEFAULT 1500000
        CHECK (rating_milli BETWEEN 0 AND 10000000),
    ADD COLUMN rating_deviation   integer NOT NULL DEFAULT 350
        CHECK (rating_deviation BETWEEN 40 AND 350),
    ADD COLUMN peak_rating        integer NOT NULL DEFAULT 1500
        CHECK (peak_rating BETWEEN 0 AND 10000),
    ADD COLUMN rated_matches      integer NOT NULL DEFAULT 0 CHECK (rated_matches >= 0),
    ADD COLUMN distinct_opponents integer NOT NULL DEFAULT 0 CHECK (distinct_opponents >= 0),
    ADD COLUMN provisional        boolean NOT NULL DEFAULT true,
    ADD COLUMN last_rated_at      timestamptz;

-- Existing code clamps with max(0,...), so legacy rows can sit in [0,100).
-- Clamp BEFORE any floor rule can turn a loss into a gain.
UPDATE player_game_ratings
   SET rating       = GREATEST(rating, 100),
       rating_milli = GREATEST(rating, 100) * 1000,
       peak_rating  = GREATEST(rating, 100);

-- Postgres integer division truncates toward zero and rating_milli >= 0, so
-- this is exactly round-half-up and matches Go's (m+500)/1000 bit for bit.
ALTER TABLE player_game_ratings
    ADD CONSTRAINT player_game_ratings_mirror_chk
        CHECK (rating = (rating_milli + 500) / 1000) NOT VALID;
ALTER TABLE player_game_ratings VALIDATE CONSTRAINT player_game_ratings_mirror_chk;

-- Remove the physical affordance for sorting a public board by the hidden number.
DROP INDEX IF EXISTS player_game_ratings_leaderboard_idx;
CREATE INDEX player_game_ratings_seeding_idx
    ON player_game_ratings (game_id, rating DESC, rated_matches DESC, user_id)
    WHERE rated_matches > 0;
COMMENT ON TABLE player_game_ratings IS
    'PRIVATE. Seeding input only. Never returned by any player-facing API.';

CREATE TABLE player_rating_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id          text NOT NULL REFERENCES games(id),
    user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    opponent_user_id uuid REFERENCES users(id),
    match_id         uuid REFERENCES matches(id) ON DELETE CASCADE,
    submission_id    uuid REFERENCES result_submissions(id),
    competition_id   uuid REFERENCES competitions(id),
    kind text NOT NULL CHECK (kind IN ('match','reversal','decay','seed_import','sanction')),
    outcome text CHECK (outcome IN ('win','draw','loss')),
    score_ppm    integer  NOT NULL CHECK (score_ppm    BETWEEN 0 AND 1000000),
    expected_ppm integer  NOT NULL CHECK (expected_ppm BETWEEN 0 AND 1000000),
    k_factor           smallint NOT NULL CHECK (k_factor >= 0),
    damping_quarters   smallint NOT NULL CHECK (damping_quarters BETWEEN 0 AND 4),
    pair_meeting_index integer  NOT NULL DEFAULT 1 CHECK (pair_meeting_index > 0),
    rating_milli_before integer NOT NULL CHECK (rating_milli_before BETWEEN 0 AND 10000000),
    rating_milli_after  integer NOT NULL CHECK (rating_milli_after  BETWEEN 0 AND 10000000),
    deviation_before smallint NOT NULL,
    deviation_after  smallint NOT NULL,
    rating_version_after bigint NOT NULL,       -- causal replay key, not id
    reverses_event_id uuid REFERENCES player_rating_events(id),
    factors           jsonb NOT NULL DEFAULT '{}'::jsonb,
    algorithm_version integer NOT NULL CHECK (algorithm_version > 0),
    occurred_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT player_rating_events_reversal_chk
        CHECK (reverses_event_id IS NULL OR kind = 'reversal')
);
-- Keyed on the SUBMISSION, so a superseding submission can rate as a new
-- event while the reversal of the old one stays in the ledger.
CREATE UNIQUE INDEX player_rating_events_one_per_submission_uidx
    ON player_rating_events (submission_id, user_id) WHERE kind = 'match';
CREATE INDEX player_rating_events_pair_idx
    ON player_rating_events (game_id, user_id, opponent_user_id, occurred_at DESC)
    WHERE kind = 'match';
CREATE INDEX player_rating_events_replay_idx
    ON player_rating_events (user_id, game_id, rating_version_after);
REVOKE UPDATE, DELETE ON player_rating_events FROM PUBLIC;

INSERT INTO player_rating_events
    (game_id,user_id,kind,score_ppm,expected_ppm,k_factor,damping_quarters,
     rating_milli_before,rating_milli_after,deviation_before,deviation_after,
     rating_version_after,algorithm_version)
SELECT game_id,user_id,'seed_import',500000,500000,0,0,
       1500000,rating_milli,350,350,rating_version,1
FROM player_game_ratings;
-- Existing ratings were produced by the float K=32 code with no history.
-- Pre-migration matches can never be truly replayed; this makes that gap an
-- explicit origin event rather than a silent one.

-- =====================================================================
-- 7. Public ladder
-- =====================================================================
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE ladder_seasons (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id text NOT NULL REFERENCES games(id),
    slug text NOT NULL,
    name text NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at   timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'upcoming'
        CHECK (status IN ('upcoming','active','closing','archived')),
    scoring jsonb NOT NULL DEFAULT '{}'::jsonb,
    scoring_version integer NOT NULL DEFAULT 1 CHECK (scoring_version > 0),
    final_snapshot_id uuid REFERENCES leaderboard_snapshots(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (starts_at < ends_at),
    CHECK (status <> 'archived' OR final_snapshot_id IS NOT NULL)
);
CREATE UNIQUE INDEX ladder_seasons_game_slug_uidx  ON ladder_seasons (game_id, lower(slug));
CREATE UNIQUE INDEX ladder_seasons_id_game_uidx    ON ladder_seasons (id, game_id);
CREATE UNIQUE INDEX ladder_seasons_one_active_uidx ON ladder_seasons (game_id) WHERE status = 'active';
ALTER TABLE ladder_seasons ADD CONSTRAINT ladder_seasons_no_overlap
    EXCLUDE USING gist (game_id WITH =, tstzrange(starts_at, ends_at, '[)') WITH &&);

ALTER TABLE competitions
    ADD COLUMN ladder_season_id  uuid REFERENCES ladder_seasons(id),
    ADD COLUMN ladder_eligible   boolean  NOT NULL DEFAULT true,
    ADD COLUMN ladder_tier       smallint NOT NULL DEFAULT 1 CHECK (ladder_tier BETWEEN 1 AND 3),
    ADD COLUMN ladder_field_size integer  CHECK (ladder_field_size IS NULL OR ladder_field_size >= 0);
CREATE UNIQUE INDEX competitions_id_ladder_season_uidx ON competitions (id, ladder_season_id);

CREATE TABLE ladder_awards (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    competition_id uuid NOT NULL,
    entry_id       uuid NOT NULL,
    season_id      uuid NOT NULL REFERENCES ladder_seasons(id),
    user_id        uuid NOT NULL REFERENCES users(id),
    placement        integer  NOT NULL CHECK (placement > 0),
    field_size       integer  NOT NULL CHECK (field_size >= 0),
    placement_points integer  NOT NULL CHECK (placement_points >= 0),
    size_multiplier  smallint NOT NULL CHECK (size_multiplier BETWEEN 0 AND 6),
    tier_multiplier  smallint NOT NULL CHECK (tier_multiplier BETWEEN 1 AND 3),
    eligibility text NOT NULL CHECK (eligibility IN
        ('scored','not_eligible','field_too_small','no_qualifying_matches',
         'too_few_opponents','minor_excluded','disqualified','competition_voided')),
    distinct_opponents integer NOT NULL CHECK (distinct_opponents >= 0),
    qualifying_matches integer NOT NULL CHECK (qualifying_matches >= 0),
    points_awarded  integer NOT NULL CHECK (points_awarded >= 0),
    scoring_version integer NOT NULL CHECK (scoring_version > 0),
    supersedes_award_id uuid REFERENCES ladder_awards(id),
    superseded_at  timestamptz,
    superseded_by  uuid REFERENCES users(id),
    awarded_at     timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (entry_id, competition_id)   REFERENCES competition_entries (id, competition_id) ON DELETE CASCADE,
    FOREIGN KEY (competition_id, season_id)  REFERENCES competitions (id, ladder_season_id),
    CHECK (eligibility = 'scored' OR points_awarded = 0),
    -- The arithmetic a player reads is a database invariant: a row cannot
    -- claim points its own printed factors do not produce.
    CHECK (eligibility <> 'scored'
           OR points_awarded = placement_points * size_multiplier * tier_multiplier)
);
-- Idempotency token AND supersede support in one index.
CREATE UNIQUE INDEX ladder_awards_live_uidx
    ON ladder_awards (competition_id, entry_id) WHERE superseded_at IS NULL;
CREATE INDEX ladder_awards_player_idx
    ON ladder_awards (user_id, season_id, awarded_at DESC) WHERE superseded_at IS NULL;

CREATE TABLE ladder_standings (
    season_id uuid NOT NULL REFERENCES ladder_seasons(id),
    game_id   text NOT NULL REFERENCES games(id),
    user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    points             integer NOT NULL DEFAULT 0 CHECK (points >= 0),
    tournaments_scored integer NOT NULL DEFAULT 0 CHECK (tournaments_scored >= 0),
    best_placement     integer CHECK (best_placement IS NULL OR best_placement > 0),
    matches_played integer NOT NULL DEFAULT 0 CHECK (matches_played >= 0),
    wins   integer NOT NULL DEFAULT 0 CHECK (wins   >= 0),
    draws  integer NOT NULL DEFAULT 0 CHECK (draws  >= 0),
    losses integer NOT NULL DEFAULT 0 CHECK (losses >= 0),
    standings_version bigint NOT NULL DEFAULT 0,
    last_award_at timestamptz,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (season_id, user_id),
    FOREIGN KEY (season_id, game_id) REFERENCES ladder_seasons (id, game_id),
    CHECK (matches_played = wins + draws + losses)
);
-- Exactly the public tiebreak order, so the board is one index scan.
CREATE INDEX ladder_standings_board_idx
    ON ladder_standings (season_id, points DESC, best_placement, tournaments_scored, user_id)
    WHERE points > 0;
CREATE INDEX ladder_standings_player_idx ON ladder_standings (user_id, season_id);

-- =====================================================================
-- 8. Leaderboard snapshots carry points, never the hidden rating.
--    DESTRUCTIVE, PRE-LAUNCH ONLY.
-- =====================================================================
DELETE FROM leaderboard_snapshot_rows;
DELETE FROM leaderboard_snapshots;
ALTER TABLE leaderboard_snapshots
    ADD COLUMN season_id uuid REFERENCES ladder_seasons(id),
    ADD COLUMN kind text NOT NULL DEFAULT 'ladder' CHECK (kind = 'ladder');
ALTER TABLE leaderboard_snapshot_rows
    ADD COLUMN points             integer CHECK (points >= 0),
    ADD COLUMN best_placement     integer CHECK (best_placement IS NULL OR best_placement > 0),
    ADD COLUMN tournaments_scored integer CHECK (tournaments_scored IS NULL OR tournaments_scored >= 0),
    ALTER COLUMN rating DROP NOT NULL,
    ADD CONSTRAINT leaderboard_rows_ladder_shape_chk
        CHECK (rating IS NULL AND points IS NOT NULL);
DROP FUNCTION IF EXISTS publish_leaderboard_snapshot(text, text, text);
-- Replaced by publish_ladder_snapshot(season_id, scope, country_code): same
-- pg_advisory_xact_lock + build-then-flip-to-ready structure as 000007, but
-- ranking FROM ladder_standings and capped at WHERE current_rank <= 10000.

COMMIT;
```

Note: `matches.bracket` has no CHECK constraint today, so `'winners'`, `'losers'`, `'grand_final'`, `'bronze'`, `'group_a'..'group_h'` are all usable with no migration. The existing `CHECK (home_entry_id IS NULL OR home_entry_id <> away_entry_id)` becomes a free backstop against a loser-drop mapping bug.

---

## Go package and module shape

```
services/api/
  cmd/api/                       (exists)
  cmd/worker/                    NEW — outbox drain, deadline sweeper, round release,
                                       snapshot publish, RD decay
  internal/bracket/              NEW — pure graph + persistence + progression
    graph.go        types, LocalRef, Slot, Node, Graph
    emit_se.go      seedOrder, single elimination + bronze
    emit_de.go      winners tree, losers tree, dropIndex, grand final + reset
    emit_rr.go      Berger circle, groups, home/away alternation
    prune.go        the ONE place byes are handled, format-independent
    rank.go         longest-path layering + canonical fingerprint
    generate.go     DrawCompetition
    progress.go     WithCompetitionGate, AdvanceFromCompletedMatch, settleChild
    standings.go    standings deltas + recursive tiebreak chain
    finalize.go     stage completion + competition_entry_placements
    unwind.go       correction subgraph unwind
    view.go         LoadBracket (admin + public)
  internal/rating/               NEW — replaces applyConfirmedResultRatings
    table.go        expectedPPM [4001]int32, //go:generate, CI byte-diff
    elo.go          pure integer update, no float, no math package
    apply.go        ApplyMatchOutcome
    seed.go         SeedKey, FreezeSeedInputs
    reverse.go      ReverseSubmission
  internal/ladder/               NEW
    scoring.go      pure Score()
    season.go       StampSeason, RolloverSeason
    award.go        AwardCompetition, RecomputeStandings
```

```go
package bracket

type Format string      // single_elimination | double_elimination | round_robin
type SourceKind uint8   // SourceEntry | SourceWinnerOf | SourceLoserOf | SourceBye (in-memory only)
type Cause string       // draw | player_confirmation | referee (superseded: legacy since v2) | timeout_forfeit |
                        // withdrawal | disqualification | admin_correction | platform_review (v2)

type LocalRef struct{ Bracket string; Round, Number int }
type Slot     struct{ Kind SourceKind; EntryID string; Src LocalRef }
type Node     struct{ Ref LocalRef; Home, Away Slot; Rank int; GroupKey string
                      Activation string; ActSrc LocalRef }
type Graph    struct{ Nodes []Node; Fingerprint [32]byte }

type DrawEntry struct{ EntryID string; SeedKey, Rating, Deviation int; GroupKey string }
type DrawInput struct {
    CompetitionID, StageID string
    Format   Format
    Entries  []DrawEntry     // ordered, immutable, canonical
    DrawSeed [32]byte
    Config   Config
    Anchor   time.Time
}

// Pure. No clock, no rand, no map iteration. A lint test asserts this.
func Emit(in DrawInput) (Graph, error)
func Prune(g Graph) Graph
func Rank(g Graph) Graph
func Fingerprint(g Graph) [32]byte

func DrawCompetition(ctx context.Context, tx pgx.Tx, in DrawInput, actorUserID string) (DrawResult, error)
func ReplayDraw(ctx context.Context, q Querier, competitionID string) (Graph, error)

type Outcome struct {
    MatchID, CompetitionID, StageID string
    StageFormat   Format
    WinnerEntryID *string          // nil = nobody advances
    HomeScore, AwayScore int
    Cause         Cause
    ActorUserID   *string
    At            time.Time
}
type Advanced    struct{ MatchID, Slot string; EntryID *string; NewState string }
type Progression struct{ Advanced []Advanced; Readied, Cancelled []string
                         StageCompleted, CompetitionFinishable bool }

// MUST be the first lock any progression-capable transaction takes.
func WithCompetitionGate(ctx context.Context, tx pgx.Tx, competitionID string, fn func() error) error

// The ONLY function in the codebase that writes home_entry_id/away_entry_id after draw time.
func AdvanceFromCompletedMatch(ctx context.Context, tx pgx.Tx, out Outcome) (Progression, error)

func ForfeitLiveMatchesForEntry(ctx context.Context, tx pgx.Tx, competitionID, entryID string,
                                cause Cause, actorUserID string, at time.Time) (Progression, error)
func UnwindSubgraph(ctx context.Context, tx pgx.Tx, matchID, actorUserID, reason string) (Progression, error)
func ReleaseRound(ctx context.Context, tx pgx.Tx, stageID string, round int, at time.Time) (int, error)
func FinalizeStage(ctx context.Context, tx pgx.Tx, stageID string) error   // idempotent, worker-driven

type Window struct{ Bracket string; RankFrom, RankTo int }
func LoadBracket(ctx context.Context, q Querier, competitionID string, w Window) (BracketView, error)
```

```go
package rating

const AlgorithmVersion = 1

func Expected(diffElo int) int32                       // parts per million, table lookup, no float
func Delta(selfMilli, oppMilli, scorePPM, k, damp int) (selfDelta, oppDelta int)
func SeedKey(rating, deviation, peak int) int          // max(rating-RD, peak-150)

type MatchInput struct {
    MatchID, SubmissionID, CompetitionID, GameID string
    SeasonID     *string
    HomeUserID, AwayUserID string
    HomeScore, AwayScore   int
    Tiebreak     *TiebreakInput
    At           time.Time
}
type Change struct{ UserID string; Before, After, K, Damping, MeetingIndex int }

// Returns changes for audit_events ONLY. The caller must never put them in a response body.
func ApplyMatchOutcome(ctx context.Context, tx pgx.Tx, in MatchInput) ([]Change, error)
func ReverseSubmission(ctx context.Context, tx pgx.Tx, submissionID, actorUserID, reason string) error
func FreezeSeedInputs(ctx context.Context, tx pgx.Tx, competitionID, gameID string) ([]bracket.DrawEntry, error)
```

```go
package ladder

type Award struct{ Placement, FieldSize, PlacementPoints, SizeMultiplier,
                   TierMultiplier, Points int; Eligibility string }

func Score(placement, fieldSize, tier, distinctOpponents, qualifyingMatches int, cfg Config) Award  // pure

func StampSeason(ctx context.Context, tx pgx.Tx, competitionID string, at time.Time) (seasonID string, fieldSize int, err error)
func AwardCompetition(ctx context.Context, tx pgx.Tx, competitionID, actorUserID string) (Awarded, error)
func SupersedeAwards(ctx context.Context, tx pgx.Tx, competitionID, actorUserID, reason string) error
func RecomputeStandings(ctx context.Context, tx pgx.Tx, seasonID string, userIDs []string) error
```

---

## Exact plug-in to `internal/httpapi/result_handlers.go`

`decideResultSubmission`, confirm branch. Seven edits.

**Edit 1 — take the gate first, immediately after `beginIdempotentRequest` and BEFORE `lockSubmissionForDecision`.**

```go
var competitionID string
if err = tx.QueryRow(r.Context(), `SELECT match.competition_id::text
    FROM result_submissions submission
    JOIN matches match ON match.id = submission.match_id
    WHERE submission.id = $1`, submissionID).Scan(&competitionID); err != nil { /* 404 / 503 */ }

if _, err = tx.Exec(r.Context(),
    `SELECT pg_advisory_xact_lock(hashtextextended('competition:' || $1, 91340287))`,
    competitionID); err != nil { /* 503 */ }
```

Order is load-bearing. With the gate second: T1 confirms M1, holds the gate, waits for a row lock on M3 (an edge target of M1); T2 is confirming M3, already holds M3's row lock from its own `lockSubmissionForDecision`, and waits for the gate. Cycle. Gate-first means nothing can hold a match row lock in this competition without already holding the gate. Enforce with a `withCompetitionGate` helper that is the only sanctioned way to open a progression-capable transaction, and route all four `Outcome` constructors through it.

**Edit 2 — `lockSubmissionForDecision` (`:683`).** Add `match.competition_id`, `match.stage_id`, `match.graph_rank`, `stage.format`, `stage.config`, `competition.ladder_season_id` to the SELECT list and `JOIN competition_stages stage ON stage.id = match.stage_id`. Extend `lockedSubmission` with the matching fields. **Do not** add `stage` to `FOR UPDATE OF submission,match` — the stage row is locked later, last, in its own statement.

**Edit 3 — the CAS at `:584`.** Add the rate-once latch and the reason:

```go
command, updateErr := tx.Exec(r.Context(), `UPDATE matches
    SET state='completed', winner_entry_id=$1, completed_at=now(),
        completion_reason='played', rated_submission_id=$4,
        version=version+1, updated_at=now()
    WHERE id=$2 AND version=$3 AND state='awaiting_confirmation'
      AND rated_submission_id IS NULL`,
    locked.Match.WinnerEntryID, locked.Match.ID, locked.Match.Version-1, submissionID)
```

The existing `RowsAffected() != 1` assert at `:587` stays. Winning this single guarded UPDATE is what grants the right to advance and the right to rate.

**Edit 4 — insert progression between the CAS and ratings** (currently line 592):

```go
var progression bracket.Progression
if err == nil {
    progression, err = bracket.AdvanceFromCompletedMatch(r.Context(), tx, bracket.Outcome{
        MatchID:       locked.Match.ID,
        CompetitionID: locked.CompetitionID,
        StageID:       locked.StageID,
        StageFormat:   bracket.Format(locked.StageFormat),
        WinnerEntryID: locked.Match.WinnerEntryID,
        HomeScore:     locked.Submission.HomeScore,
        AwayScore:     locked.Submission.AwayScore,
        Cause:         bracket.CausePlayerConfirmation,
        ActorUserID:   &userID,
        At:            decisionTime,
    })
}
```

Progression **before** ratings, in every caller. `player_game_ratings` is the only cross-competition resource in the transaction; it is acquired last and held briefest.

**Edit 5 — replace the rating call at `:592`:**

```go
var ratingChanges []rating.Change
if err == nil {
    ratingChanges, err = rating.ApplyMatchOutcome(r.Context(), tx, rating.MatchInput{
        MatchID: locked.Match.ID, SubmissionID: submissionID,
        CompetitionID: locked.CompetitionID, GameID: locked.GameID,
        SeasonID: locked.LadderSeasonID,
        HomeUserID: locked.HomePlayerID, AwayUserID: locked.AwayPlayerID,
        HomeScore: locked.Submission.HomeScore, AwayScore: locked.Submission.AwayScore,
        Tiebreak: locked.Submission.Tiebreak, At: decisionTime,
    })
}
```

`applyConfirmedResultRatings` is deleted. Its `sort.Strings` + `ORDER BY user_id FOR UPDATE` discipline is preserved verbatim inside `rating.ApplyMatchOutcome` — it is already correct and it is what prevents A-B/B-A deadlock in a parallel round robin.

**Edit 6 — split `writeResultAuditAndOutbox` (`:978`) into `(auditPayload, outboxPayload)`.** It currently writes one payload to both tables. Rating deltas stay in `audit_events.after_state` (staff need them for appeals; *[Superseded by result verification v2]*: now for Gamics result reviews) and are **stripped from `outbox_events.payload`**, which drives push notifications. Without this split the "hidden" rating leaks through the notification pipeline. The outbox payload gains `advancedMatchIds` and `readiedMatchIds`.

**Edit 7 — after `tx.Commit`:**

```go
s.responses.Invalidate(r.Context(), "competition-bracket:"+competitionID, "competition-detail:"+competitionID)
```

`decideResultSubmission` invalidates nothing today, so a confirmed result is currently invisible on the bracket for up to `KeepFor=20s`. Pre-existing bug; this work fixes it.

**Delete:** `RatingChanges` from `resultDecisionView` (`:113`) and the whole `ratingChangeView` struct (`:116`). OpenAPI break, mobile change. Also delete the `"ratingChanges"` key from the outbox payload.

**Global lock order** (asserted by a stress test and a static test):

```
L0  idempotency_keys                    (beginIdempotentRequest, :504)
L1  pg_advisory_xact_lock(competition)  NEW — the gate
L2  evidence_uploads                    (per-user, no cross-user contention)
L3  result_submissions + the decided match  (FOR UPDATE OF submission,match, :702)
L4  child matches, ascending (graph_rank, id)
L5  competition_stages, its own statement, LAST before the completeness probe
L6  competition_standings, ascending entry_id
L7  player_game_ratings, ascending user_id   (:740, already correct)
```

The dispute branch (`:598-617`) calls no progression and no rating. Correct as written: a disputed match is not completed and must not advance. *[Superseded by result verification v2]* That branch is gone; a mismatched or reviewed match still stays unresolved, with no progression or rating, until its reports agree, a deadline removes an entry, or Gamics decides.

---

## Delivery order

| # | Work | Effort | What staff and players actually feel |
|---|---|---|---|
| 1 | Migration 000009 + `internal/bracket` pure core (emitters, prune, rank, fingerprint) + the structural/determinism property suite | 1.5w | Nothing. Do not skip it — every later week rests on `Emit` being pure and exhaustively tested to N=1024 without a database. |
| 2 | `DrawCompetition` + `POST /v1/organizations/{orgId}/competitions/{competitionId}/draw` + `GET .../bracket` (organizer, writer-read, `?bracket=&rankFrom=&rankTo=`) + raise `queryCompetitionBracket`'s `LIMIT 1000` and add a row-count overflow assertion | 1.5w | **First felt thing.** Staff press "Generate draw" during check-in and see the **entire** bracket including every future round, with "Winner of W-R2-M3" placeholders drawn from `match_slots`. `assertTransitionReady` stops returning 409 `bracket_not_generated`, so events can start. A 1024-entry DE stops silently truncating at 1000 rows. |
| 3 | `WithCompetitionGate` + `AdvanceFromCompletedMatch` + `settleChild` + the seven `result_handlers.go` edits + cache invalidation | 1.5w | Players confirm a result and their next match appears in `/v1/me/matches` immediately. Staff watch the bracket fill live instead of at a 20-second cache lag. |
| 4 | `cmd/worker`: outbox drain, `result_due_at` forfeit sweeper, round release, idempotent `FinalizeStage`. Plus three staff routes — `POST .../matches/{id}/forfeit` (`PermissionEntryManage`), `POST .../matches/{id}/result` (`PermissionDisputeResolve`), `POST .../disputes/{id}/resolution` *[Superseded by result verification v2]* (organizers never decide results; the result and resolution routes were replaced by the Gamics review queue) | 2w | **The one that matters operationally.** A no-show no longer stalls a branch forever. Staff can resolve a dispute, set a score, and DQ a cheater — and the bracket settles both DE branches from one action. Push notifications start working at all. |
| 5 | Round-robin groups (`bracket='group_a'..`), `competition_standings`, recursive tiebreak chain, per-round release, placements | 1.5w | Leagues are runnable round by round instead of dumping 190 simultaneously-live fixtures. Group-stage tables appear. |
| 6 | `internal/rating`: integer table, ledger, `ApplyMatchOutcome`, `SeedKey`, delete the public Elo from the API and the outbox | 1.5w | Draws stop being random. A smurf seeds at 1150 and meets a top seed in round one. The confusing per-match rating number disappears from the app. |
| 7 | `internal/ladder`: seasons, awards, standings, `publish_ladder_snapshot`, `GET /v1/players/{id}/points` | 1.5w | The public board becomes a points ladder every player can add up line by line. **That endpoint IS the trust argument** — without it the ladder is just another opaque number. |
| 8 | Corrections: `UnwindSubgraph`, `ReverseSubmission`, `SupersedeAwards` + `RecomputeStandings`, admin console polish | 1.5w | Staff can fix a wrong winner without hand-editing SQL. |

**~12 weeks.** MBG estimated 6 and Format Plugin 8; both excluded the workers they called hard prerequisites and both excluded the entire staff-intervention surface. If the number must be cut, cut the bracket reset (item 2), the bronze match, and round-robin groups (item 5) — **never** the worker or the staff endpoints. A first-party operator can live without a reset match. It cannot live without the ability to resolve a no-show.

---

## Top 3 risks and the tests that catch them

**1. Order-dependent permanent stall, and silently lost stage finalization.** Two distinct bugs with the same signature: nothing errors, the event just stops. A child with one voided slot and one filled slot sits pending forever; or the last two matches of a stage confirm concurrently, each snapshot sees the other open, and *neither* writes placements — while `assertTransitionReady` still lets staff mark the competition completed, so it surfaces months later when a player opens their history.

- `TestSettleChildIsOrderIndependent` — for every DE with two feeders, drive both completion orders including void-first, and assert the final `(state, winner_entry_id, slot resolutions, schedule timestamps)` are byte-identical. The order-agnostic property is the *only* thing that catches this class.
- `TestConcurrentStageFinalization` — 500 trials driving a stage's last two matches to concurrent commit; assert `competition_stages.status='completed'` is set exactly once and `competition_entry_placements` is non-empty. Assert the count is never **zero**, which is the actual failure mode.
- `TestPlacementsFormAValidRanking` — for all three formats, N ∈ [2,256]: every entry has exactly one row, placement 1 is unique, ties are legal, no gaps.

**2. Deadlock or gate stall at a round boundary.** The busiest instant in an event is a mass `result_due_at` forfeit sweep at a round boundary, which is exactly when multi-level void cascades fire. Deadlock freedom rests on the gate, and the gate's cost is bounded only if nothing slow runs inside it.

- `TestNoDeadlocksUnderStress` — 64 workers confirming random ready matches across 8 simultaneous 256-entry double eliminations for 60 seconds; assert zero SQLSTATE `40P01`.
- `TestGateOrderingIsLoadBearing` — re-run the above with the gate deliberately moved *after* `lockSubmissionForDecision` and assert the test **fails**. A passing deadlock test proves nothing unless you can show it detects the bug.
- `TestGateHoldTimeBudget` — assert p99 gate hold under 25ms, and a static test that no S3, Redis, or HTTP call appears in any code path reachable from `WithCompetitionGate`. Ship a 250ms `statement_timeout` on progression queries and a **5s `lock_timeout`** on the gate acquisition itself — `pg_advisory_xact_lock` is a statement, so one timeout for both would abort waiters rather than the slow holder.
- `TestCascadeIsBounded` — a chain of withdrawals producing a cascade deeper than the bound aborts and commits nothing rather than running unbounded work inside an HTTP request.

**3. A correction after advancement corrupting history, ratings, or the ladder.** The hardest case, and the one both losing proposals got wrong in schema.

- `TestStandingsEqualsSumOfLiveAwards` — for every `(season_id, user_id)`: `ladder_standings.points == SUM(ladder_awards.points_awarded) WHERE superseded_at IS NULL`. **Ship this as a SQL assertion CI runs after every integration suite, not just the ladder ones.** Any bug in awarding, rollover, DQ or correction breaks it. This is the assertion the original design shipped in a form that would fail the first time staff fixed anything.
- `TestCorrectionRefusesOrUnwindsCleanly` — if any match in the recursive-CTE closure is past `ready`, assert 409 `downstream_matches_started` and a byte-identical database. When downstream is untouched, assert the subgraph unwinds, refills, and re-passes the entire structural invariant suite.
- `TestRatingReplayIsBitIdentical` — rebuild `player_game_ratings` by replaying `player_rating_events` ordered by `(user_id, game_id, rating_version_after)`, **recomputing** each delta from stored `(rating_milli_before, k_factor, expected_ppm, score_ppm, damping_quarters)` rather than reading `rating_milli_after`. Run it *after* a correction, not just on a clean log.
- `TestNoFloatsInRating` — walk the `go/ast` of `internal/rating` and assert zero `float32`/`float64` and zero calls into `math`. Cheap, and it is the only thing that keeps the cross-platform determinism claim true a year from now when someone "simplifies" the table lookup back into `math.Pow`.
- `TestHiddenRatingNeverLeaves` — hit every route in `server.go` anonymously, as a player, and as an organizer; assert no response body and no `outbox_events.payload` contains `rating`, `ratingChanges`, `mmr` or `elo`, and no numeric value matches any `player_game_ratings.rating` in the fixture set. **This test fails against the code as it stands today**, which is exactly why it is worth writing first.
- `TestDeterministicReplayAfterEntryMutation` — disqualify an entry *after* the draw and assert `ReplayDraw` still reproduces the stored `graph_fingerprint`. This is the test the original MBG determinism test was written around rather than through.

---

## What each choice gives up

**Choosing MBG:**
- **The entry set freezes at draw time.** A late arrival means redraw or refusal, and redraw is only legal before any match starts. Staff *will* ask. A lazy-generation design would handle this better; nothing else would.
- **`graph_rank` is immutable forever.** The composite FK `(source_match_id, source_rank) → matches(id, graph_rank)` is what makes cycles unrepresentable in DDL — and it also forbids ever re-ranking a match or inserting one into a live bracket. A one-way door, taken deliberately.
- **Byes are not materialized**, so a 1024-slot bracket with 700 entries has no full power-of-two grid. Any renderer that assumes every `(round, position)` exists breaks. Clients must lay out from `match_slots` edges and `graph_rank` columns. Real front-end cost; the alternative is fabricating phantom forfeit rows that pollute player records.
- **`if_source_away_wins` is a special case wearing a general name.** A future play-in or conditional decider needs a schema change, not data. Chosen over a jsonb predicate language because an open predicate language is un-CHECK-able.
- **The loser-drop mapping is a heuristic.** Alternate-round reversal reduces early rematches; it does not eliminate them for all N, particularly for non-power-of-two fields where the prune pass removed matches. Do not claim rematch-freedom.
- **Deadlock freedom now rests on the gate, which is a serialization point.** A 1024-entry round-1 burst is ~2.8 confirmations/sec through one mutex at ~2-5ms each — under 2% utilization — but any slow statement that ever enters the gate blocks every confirmation in that event.

**Rejecting Format Plugin:**
- **No per-stage driver interface**, so a fourth format costs a redesign. Swiss in particular pairs a *new* round from standings — it materializes matches at runtime, which this design forbids in order to keep the plan fingerprint a complete description of the event. Group-into-knockout needs the deferred `source_kind='standing'` edge (one ALTER, one emitter) rather than falling out of the model.

**Choosing Two-Layer:**
- **Competitive players lose the per-match rank-up loop.** Placement-points-per-tournament is a far slower dopamine cycle than per-match rating movement. Expect churn among exactly the high-skill players whose retention matters most. There is no free fix: rendering the hidden number as tiers ships a second public ladder and inherits every "why did my tier drop when I won" ticket.
- **Season rollover empties the board on day one of every season.** The fix (previous-season tiebreak plus an all-time tab) reintroduces a third number, partially undoing the whole one-number argument.
- **Points reward participation.** Someone entering 30 small events beats someone who wins two majors; size and tier multipliers only partly compensate. Tennis-style best-N scoring is more accurate and was rejected because a player's total can *drop* when an old result ages out — producing the exact "why did my points go down when I won" ticket the design exists to avoid. Explainability beat accuracy, on purpose.
- **No margin of victory.** Scores are self-reported and merely opponent-confirmed, so a colluding pair reports 9-0 as cheaply as 1-0. Binary outcomes bound the damage of any fabricated match to K, at the cost of real signal — in eFootball, scorelines are informative. Revisit if publisher-verified results ever arrive.
- **Per-player K makes the hidden rating non-zero-sum**, so it is not comparable across eras and could never be safely published later without recalibration. Since it is permanently hidden, accepted — but it forecloses that option.
- **The public board is empty by default.** `player_profiles.discoverable` DEFAULTS TO FALSE and the snapshot builder joins on it. A public ladder nobody appears on is worse than no ladder. This needs a product decision I am flagging, not making: I recommend entering a public competition sets `discoverable=true` with disclosure at registration and a one-tap opt-out, minors excluded entirely with `eligibility='minor_excluded'` recording the reason on the award row.
- **Capping snapshots at rank 10,000** is a behaviour change: deep pagination past 10k stops working and those players' ranks become point-in-time rather than snapshot-consistent.

**Rejecting TWE:**
- **No public skill number for marketing or sponsors.** A "top 100 rated players in Kenya" page is not available, and the hidden rating's non-zero-sum property means it can never become one without recalibration.
- **The anti-farm gate is weaker in one specific way.** TWE's per-competition net gain cap directly bounds how much a single event can move a player. The ladder has no equivalent — it bounds farming by *placement pays once per event* plus `minField=8` plus a rolling-30-day pair window, which is a different and coarser instrument. A ring of eight running weekly is stopped; a ring of eight running monthly, at threshold 6, is not. Named, monitored, not solved.

**Both:**
- **Neither layer is safe against a truly determined attacker with weak identity.** One SIM plus one M-Pesa entry fee buys a smurf. This makes smurfing *unprofitable* (gains against unknowns round to zero via the expectation term, not a special case) and *detectable* (pair-concentration and first-five-matches pattern flags). Any stronger claim would be false, and the graph-analysis worker that detects rings does not exist and is not in the 12 weeks.
