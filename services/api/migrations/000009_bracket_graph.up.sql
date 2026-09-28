BEGIN;

-- Bracket dependency graph.
--
-- Until now `matches` recorded a round and a match number but had no way to say
-- where its two participants come from. Progression therefore had to be inferred
-- from round arithmetic, which docs/architecture.md rules out, and in practice
-- nothing could create a match at all.
--
-- This migration adds the edges. It deliberately carries ONLY the bracket graph.
-- The rating and ladder sections of docs/algorithm-decision.md are held back:
-- their `player_game_ratings_mirror_chk` requires rating = (rating_milli+500)/1000,
-- while the live applyConfirmedResultRatings writes `rating` and knows nothing of
-- `rating_milli`. Shipping them together would make every result confirmation
-- fail a check constraint. They land with internal/rating that maintains both.

-- =====================================================================
-- 1. matches: graph metadata and the composite targets its FKs need
-- =====================================================================
ALTER TABLE matches
    ADD COLUMN graph_rank integer NOT NULL DEFAULT 1,
    ADD COLUMN group_key text,
    ADD COLUMN activation_rule text NOT NULL DEFAULT 'unconditional'
        CHECK (activation_rule IN ('unconditional', 'if_source_away_wins')),
    ADD COLUMN activation_source_match_id uuid,
    ADD COLUMN completion_reason text CHECK (completion_reason IN
        ('played', 'walkover', 'double_no_show', 'referee', 'timeout_forfeit',
         'reset_not_required', 'correction_voided')),
    ADD CONSTRAINT matches_graph_rank_positive_chk CHECK (graph_rank > 0),
    -- A conditional match must name the match whose outcome activates it, and an
    -- unconditional one must not pretend to have a condition.
    ADD CONSTRAINT matches_activation_pair_chk CHECK (
        (activation_rule = 'unconditional' AND activation_source_match_id IS NULL) OR
        (activation_rule <> 'unconditional' AND activation_source_match_id IS NOT NULL));

-- Safe because nothing has ever written a row here: the default existed only to
-- satisfy NOT NULL during the ALTER.
ALTER TABLE matches ALTER COLUMN graph_rank DROP DEFAULT;

-- Three to four UPDATEs per match lifecycle (ready, in progress, awaiting
-- confirmation, completed). Leaving page room keeps those updates HOT.
ALTER TABLE matches SET (fillfactor = 85);

-- 000004 gave competition_stages and competition_entries composite targets so
-- child rows could be pinned to the same competition. matches never got them,
-- and every edge below depends on them.
CREATE UNIQUE INDEX matches_id_competition_uidx ON matches (id, competition_id);
CREATE UNIQUE INDEX matches_id_rank_uidx ON matches (id, graph_rank);

CREATE INDEX matches_stage_open_idx ON matches (stage_id)
    WHERE state NOT IN ('completed', 'forfeit', 'cancelled');
CREATE INDEX matches_stage_graph_idx ON matches (stage_id, bracket, graph_rank, match_number);
CREATE INDEX matches_deadline_idx ON matches (result_due_at)
    WHERE state IN ('ready', 'in_progress') AND result_due_at IS NOT NULL;
CREATE INDEX matches_release_idx ON matches (stage_id, round_number)
    WHERE state = 'pending' AND check_in_opens_at IS NOT NULL;

-- =====================================================================
-- 2. match_slots: the dependency edges themselves
--
--    source_* is written once at draw time and never changes. Only
--    resolved_*/voided_at move. One model covers all three formats:
--      single_elimination : winner_of edges, plus two loser_of for a bronze
--      double_elimination : winner_of + loser_of + one activation rule
--      round_robin        : every slot is 'entry', so there are no edges
-- =====================================================================
CREATE TABLE match_slots (
    match_id uuid NOT NULL,
    competition_id uuid NOT NULL,
    slot text NOT NULL CHECK (slot IN ('home', 'away')),
    match_rank integer NOT NULL CHECK (match_rank > 0),
    source_kind text NOT NULL CHECK (source_kind IN ('entry', 'winner_of', 'loser_of')),
    source_entry_id uuid,
    source_match_id uuid,
    source_rank integer,
    resolved_entry_id uuid,
    resolved_at timestamptz,
    voided_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (match_id, slot),
    FOREIGN KEY (match_id, competition_id) REFERENCES matches (id, competition_id) ON DELETE CASCADE,
    FOREIGN KEY (match_id, match_rank) REFERENCES matches (id, graph_rank),
    FOREIGN KEY (source_match_id, competition_id) REFERENCES matches (id, competition_id),
    FOREIGN KEY (source_match_id, source_rank) REFERENCES matches (id, graph_rank),
    FOREIGN KEY (source_entry_id, competition_id) REFERENCES competition_entries (id, competition_id),
    FOREIGN KEY (resolved_entry_id, competition_id) REFERENCES competition_entries (id, competition_id),
    CONSTRAINT match_slots_shape_chk CHECK (
        (source_kind = 'entry'
            AND source_entry_id IS NOT NULL AND source_match_id IS NULL AND source_rank IS NULL) OR
        (source_kind IN ('winner_of', 'loser_of')
            AND source_entry_id IS NULL AND source_match_id IS NOT NULL AND source_rank IS NOT NULL)),
    -- Acyclicity is a schema fact rather than a generator convention: an edge may
    -- only ever point at a strictly earlier layer.
    CONSTRAINT match_slots_acyclic_chk CHECK (source_rank IS NULL OR source_rank < match_rank),
    CONSTRAINT match_slots_not_self_chk CHECK (source_match_id IS NULL OR source_match_id <> match_id),
    CONSTRAINT match_slots_resolution_chk CHECK ((resolved_entry_id IS NULL) = (resolved_at IS NULL)),
    -- A slot is either filled or dead, never both.
    CONSTRAINT match_slots_exclusive_chk CHECK (NOT (resolved_at IS NOT NULL AND voided_at IS NOT NULL))
);

-- A match's winner feeds at most one slot, and so does its loser. Keying on
-- source_kind rather than source_match_id alone admits the two legitimate
-- same-source cases: the double-elimination grand final when N=2, and the
-- bracket reset.
CREATE UNIQUE INDEX match_slots_one_consumer_uidx
    ON match_slots (source_match_id, source_kind) WHERE source_match_id IS NOT NULL;
CREATE INDEX match_slots_open_source_idx
    ON match_slots (source_match_id) WHERE resolved_at IS NULL AND voided_at IS NULL;
CREATE INDEX match_slots_competition_idx ON match_slots (competition_id);

-- =====================================================================
-- 3. Draw provenance and the frozen entry vector replay depends on
-- =====================================================================
CREATE TABLE competition_draws (
    competition_id uuid PRIMARY KEY REFERENCES competitions(id) ON DELETE CASCADE,
    stage_id uuid NOT NULL,
    algorithm text NOT NULL,
    algorithm_version integer NOT NULL CHECK (algorithm_version > 0),
    draw_seed bytea NOT NULL CHECK (octet_length(draw_seed) = 32),
    seeding_policy text NOT NULL
        CHECK (seeding_policy IN ('seeded', 'rating', 'random', 'registration_order')),
    entry_count integer NOT NULL CHECK (entry_count >= 2),
    entry_fingerprint bytea NOT NULL CHECK (octet_length(entry_fingerprint) = 32),
    graph_fingerprint bytea NOT NULL CHECK (octet_length(graph_fingerprint) = 32),
    match_count integer NOT NULL CHECK (match_count > 0),
    config jsonb NOT NULL DEFAULT '{}'::jsonb,
    generated_by uuid NOT NULL REFERENCES users(id),
    generated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (stage_id, competition_id) REFERENCES competition_stages (id, competition_id)
);
-- The primary key is the exactly-once guard: two concurrent draw requests cannot
-- both succeed even if one forgets to lock the competition row.

CREATE TABLE competition_draw_entries (
    competition_id uuid NOT NULL REFERENCES competition_draws(competition_id) ON DELETE CASCADE,
    draw_position integer NOT NULL CHECK (draw_position > 0),
    entry_id uuid NOT NULL,
    group_key text,
    seed_key integer NOT NULL,
    rating_snapshot integer,
    deviation_snapshot integer,
    PRIMARY KEY (competition_id, draw_position),
    UNIQUE (competition_id, entry_id),
    FOREIGN KEY (entry_id, competition_id) REFERENCES competition_entries (id, competition_id)
);
-- This replaces the obvious "UPDATE competition_entries SET seed = draw position".
-- competition_entries_seed_uidx makes seeds unique per competition, so any sparse
-- organizer seeding collides with a contiguous draw position. Storing the ordered
-- vector here is also what makes graph_fingerprint replayable.

-- =====================================================================
-- 4. Round-robin and group standings, pre-created at draw time
-- =====================================================================
CREATE TABLE competition_standings (
    stage_id uuid NOT NULL,
    competition_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    group_key text NOT NULL DEFAULT 'main',
    played integer NOT NULL DEFAULT 0 CHECK (played >= 0),
    wins integer NOT NULL DEFAULT 0 CHECK (wins >= 0),
    draws integer NOT NULL DEFAULT 0 CHECK (draws >= 0),
    losses integer NOT NULL DEFAULT 0 CHECK (losses >= 0),
    walkovers integer NOT NULL DEFAULT 0 CHECK (walkovers >= 0),
    goals_for integer NOT NULL DEFAULT 0 CHECK (goals_for >= 0),
    goals_against integer NOT NULL DEFAULT 0 CHECK (goals_against >= 0),
    points integer NOT NULL DEFAULT 0,
    standings_version bigint NOT NULL DEFAULT 0 CHECK (standings_version >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (stage_id, entry_id),
    FOREIGN KEY (stage_id, competition_id) REFERENCES competition_stages (id, competition_id),
    FOREIGN KEY (entry_id, competition_id) REFERENCES competition_entries (id, competition_id),
    CHECK (played = wins + draws + losses)
);
-- Exactly the published table order, so rendering a group is one index scan.
CREATE INDEX competition_standings_table_idx ON competition_standings
    (stage_id, group_key, points DESC, (goals_for - goals_against) DESC, goals_for DESC, entry_id);

-- =====================================================================
-- 5. Progression audit: why is this player in this match
-- =====================================================================
CREATE TABLE progression_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    competition_id uuid NOT NULL REFERENCES competitions(id) ON DELETE CASCADE,
    stage_id uuid NOT NULL,
    -- Deliberately no foreign keys on the match columns. This is an audit trail
    -- and it must survive a draw being regenerated underneath it.
    source_match_id uuid,
    target_match_id uuid,
    target_slot text CHECK (target_slot IN ('home', 'away')),
    entry_id uuid,
    kind text NOT NULL CHECK (kind IN
        ('slot_filled', 'slot_voided', 'match_readied', 'match_forfeited', 'match_cancelled',
         'round_released', 'stage_completed', 'placements_written', 'subgraph_unwound')),
    cause text NOT NULL CHECK (cause IN
        ('draw', 'player_confirmation', 'referee', 'timeout_forfeit', 'withdrawal',
         'disqualification', 'admin_correction')),
    actor_user_id uuid REFERENCES users(id),
    detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX progression_events_competition_idx ON progression_events (competition_id, id DESC);

COMMIT;
