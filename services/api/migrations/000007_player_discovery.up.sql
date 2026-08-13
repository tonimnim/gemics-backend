BEGIN;

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- This is the write-side rating projection. A confirmed-result transaction owns
-- updates to this row; public reads never derive ratings from the match tables.
CREATE TABLE player_game_ratings (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    game_id text NOT NULL REFERENCES games(id),
    rating integer NOT NULL DEFAULT 1500 CHECK (rating BETWEEN 0 AND 10000),
    matches_played integer NOT NULL DEFAULT 0 CHECK (matches_played >= 0),
    wins integer NOT NULL DEFAULT 0 CHECK (wins >= 0),
    draws integer NOT NULL DEFAULT 0 CHECK (draws >= 0),
    losses integer NOT NULL DEFAULT 0 CHECK (losses >= 0),
    rating_version bigint NOT NULL DEFAULT 0 CHECK (rating_version >= 0),
    last_match_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, game_id),
    CHECK (matches_played = wins + draws + losses)
);
CREATE INDEX player_game_ratings_leaderboard_idx
    ON player_game_ratings (game_id, rating DESC, wins DESC, user_id)
    WHERE matches_played > 0;

-- Ready snapshots and their rows are append-only projections. Cursors bind to a
-- snapshot id, so later rating changes cannot duplicate or skip rows mid-scroll.
CREATE TABLE leaderboard_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id text NOT NULL REFERENCES games(id),
    scope text NOT NULL CHECK (scope IN ('global', 'country')),
    country_code char(2),
    status text NOT NULL DEFAULT 'building' CHECK (status IN ('building', 'ready')),
    source_version bigint NOT NULL DEFAULT 0 CHECK (source_version >= 0),
    snapshot_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (scope = 'global' AND country_code IS NULL)
        OR (scope = 'country' AND country_code IS NOT NULL)
    )
);
CREATE INDEX leaderboard_snapshots_latest_idx
    ON leaderboard_snapshots (game_id, scope, country_code, snapshot_at DESC, id DESC)
    WHERE status = 'ready';
CREATE INDEX leaderboard_snapshots_retention_idx
    ON leaderboard_snapshots (created_at, id);

CREATE TABLE leaderboard_snapshot_rows (
    snapshot_id uuid NOT NULL REFERENCES leaderboard_snapshots(id) ON DELETE CASCADE,
    rank integer NOT NULL CHECK (rank > 0),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    rating integer NOT NULL CHECK (rating BETWEEN 0 AND 10000),
    matches_played integer NOT NULL CHECK (matches_played > 0),
    wins integer NOT NULL CHECK (wins >= 0),
    draws integer NOT NULL CHECK (draws >= 0),
    losses integer NOT NULL CHECK (losses >= 0),
    rank_movement integer NOT NULL DEFAULT 0,
    PRIMARY KEY (snapshot_id, rank),
    UNIQUE (snapshot_id, user_id),
    CHECK (matches_played = wins + draws + losses)
);
CREATE INDEX leaderboard_snapshot_rows_player_idx
    ON leaderboard_snapshot_rows (user_id, snapshot_id);

-- Progression writes a row only after it has finalized a competition finish. The
-- public API returns null in its absence instead of presenting a bracket seed as
-- a placement. Ties remain possible, so placement is intentionally not unique.
CREATE TABLE competition_entry_placements (
    competition_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    placement integer NOT NULL CHECK (placement > 0),
    finalized_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (competition_id, entry_id),
    FOREIGN KEY (entry_id, competition_id)
        REFERENCES competition_entries (id, competition_id) ON DELETE CASCADE
);
CREATE INDEX competition_entry_placements_finish_idx
    ON competition_entry_placements (competition_id, placement, entry_id);

-- The projection worker calls this function in its transaction. Advisory locking
-- permits only one publisher for a game/scope/country while retaining historical
-- snapshots for in-flight cursors. Positive rank_movement means the player moved up.
CREATE OR REPLACE FUNCTION publish_leaderboard_snapshot(
    requested_game_id text,
    requested_scope text,
    requested_country_code text DEFAULT NULL
) RETURNS uuid
LANGUAGE plpgsql
AS $$
DECLARE
    new_snapshot_id uuid;
    previous_snapshot_id uuid;
    normalized_country char(2);
    maximum_source_version bigint;
BEGIN
    IF requested_scope IS NULL OR requested_scope NOT IN ('global', 'country') THEN
        RAISE EXCEPTION 'leaderboard scope must be global or country';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM games WHERE id = requested_game_id) THEN
        RAISE EXCEPTION 'unknown game id';
    END IF;

    IF requested_scope = 'country' THEN
        IF requested_country_code IS NULL OR upper(requested_country_code) !~ '^[A-Z]{2}$' THEN
            RAISE EXCEPTION 'country leaderboard requires a two-letter country code';
        END IF;
        normalized_country := upper(requested_country_code);
    ELSIF requested_country_code IS NOT NULL THEN
        RAISE EXCEPTION 'global leaderboard cannot have a country code';
    END IF;

    PERFORM pg_advisory_xact_lock(hashtextextended(
        requested_game_id || ':' || requested_scope || ':' || coalesce(normalized_country::text, ''),
        70419827
    ));

    SELECT id INTO previous_snapshot_id
    FROM leaderboard_snapshots
    WHERE game_id = requested_game_id
      AND scope = requested_scope
      AND country_code IS NOT DISTINCT FROM normalized_country
      AND status = 'ready'
    ORDER BY snapshot_at DESC, id DESC
    LIMIT 1;

    SELECT coalesce(max(rating_version), 0) INTO maximum_source_version
    FROM player_game_ratings
    WHERE game_id = requested_game_id;

    INSERT INTO leaderboard_snapshots
        (game_id, scope, country_code, status, source_version)
    VALUES
        (requested_game_id, requested_scope, normalized_country, 'building', maximum_source_version)
    RETURNING id INTO new_snapshot_id;

    WITH ranked AS (
        SELECT
            rating.user_id,
            rating.rating,
            rating.matches_played,
            rating.wins,
            rating.draws,
            rating.losses,
            row_number() OVER (
                ORDER BY rating.rating DESC, rating.wins DESC, rating.user_id
            )::integer AS current_rank
        FROM player_game_ratings rating
        JOIN users player ON player.id = rating.user_id
        JOIN player_profiles profile ON profile.user_id = player.id
        WHERE rating.game_id = requested_game_id
          AND rating.matches_played > 0
          AND player.status = 'active'
          AND profile.discoverable = true
          AND (requested_scope = 'global' OR player.country_code = normalized_country)
    )
    INSERT INTO leaderboard_snapshot_rows
        (snapshot_id, rank, user_id, rating, matches_played, wins, draws, losses, rank_movement)
    SELECT
        new_snapshot_id,
        ranked.current_rank,
        ranked.user_id,
        ranked.rating,
        ranked.matches_played,
        ranked.wins,
        ranked.draws,
        ranked.losses,
        coalesce(previous.rank - ranked.current_rank, 0)
    FROM ranked
    LEFT JOIN leaderboard_snapshot_rows previous
      ON previous.snapshot_id = previous_snapshot_id
     AND previous.user_id = ranked.user_id
    ORDER BY ranked.current_rank;

    UPDATE leaderboard_snapshots
    SET status = 'ready'
    WHERE id = new_snapshot_id;

    RETURN new_snapshot_id;
END;
$$;

-- Launch-scale public search indexes. The partial predicates mirror the handler's
-- privacy/status filters; `%term%` search remains bounded by a mandatory page size.
CREATE INDEX player_profiles_discoverable_handle_trgm_idx
    ON player_profiles USING gin (lower(handle) gin_trgm_ops)
    WHERE discoverable = true;
CREATE INDEX users_active_display_name_trgm_idx
    ON users USING gin (lower(display_name) gin_trgm_ops)
    WHERE status = 'active';
CREATE INDEX game_accounts_in_game_name_trgm_idx
    ON game_accounts USING gin (lower(in_game_name) gin_trgm_ops);
CREATE INDEX entry_members_user_history_idx
    ON entry_members (user_id, competition_id, entry_id)
    WHERE roster_role IN ('starter', 'substitute');
CREATE INDEX matches_public_history_idx
    ON matches (completed_at DESC, id DESC)
    WHERE state IN ('completed', 'forfeit');

COMMIT;
