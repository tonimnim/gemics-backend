BEGIN;

-- Games, players' game accounts, ratings and leaderboards.

-- Functions

CREATE FUNCTION publish_leaderboard_snapshot(requested_game_id text, requested_scope text, requested_country_code text DEFAULT NULL::text) RETURNS uuid
    LANGUAGE plpgsql
    AS $_$
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
$_$;

-- Sequences

CREATE SEQUENCE player_game_rating_change_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 100;

-- games

CREATE TABLE games (
    id text NOT NULL,
    name text NOT NULL,
    publisher text NOT NULL,
    active boolean DEFAULT true NOT NULL,
    supported_platforms text[] NOT NULL,
    result_mode text NOT NULL,
    rules_schema jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT games_result_mode_check CHECK ((result_mode = ANY (ARRAY['participant_confirmation'::text, 'publisher_api'::text, 'admin_only'::text])))
);
ALTER TABLE ONLY games
    ADD CONSTRAINT games_pkey PRIMARY KEY (id);

-- game_accounts

CREATE TABLE game_accounts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    game_id text NOT NULL,
    platform text DEFAULT ''::text NOT NULL,
    in_game_name text NOT NULL,
    publisher_player_id text,
    verification_status text DEFAULT 'unverified'::text NOT NULL,
    verified_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    verification_method text,
    publisher_verified boolean DEFAULT false NOT NULL,
    CONSTRAINT game_accounts_publisher_player_id_check CHECK (((publisher_player_id IS NULL) OR (char_length(publisher_player_id) <= 64))),
    CONSTRAINT game_accounts_publisher_verified_chk CHECK (((NOT publisher_verified) OR ((verification_status = 'verified'::text) AND (verification_method = 'publisher_api'::text)))),
    CONSTRAINT game_accounts_verification_method_check CHECK ((verification_method = ANY (ARRAY['manual_evidence'::text, 'publisher_api'::text]))),
    CONSTRAINT game_accounts_verification_status_check CHECK ((verification_status = ANY (ARRAY['unverified'::text, 'pending'::text, 'verified'::text, 'rejected'::text])))
);
ALTER TABLE ONLY game_accounts
    ADD CONSTRAINT game_accounts_pkey PRIMARY KEY (id);
CREATE UNIQUE INDEX game_accounts_id_user_uidx ON game_accounts USING btree (id, user_id);
CREATE INDEX game_accounts_in_game_name_trgm_idx ON game_accounts USING gin (lower(in_game_name) gin_trgm_ops);
CREATE UNIQUE INDEX game_accounts_publisher_id_unique ON game_accounts USING btree (game_id, upper(regexp_replace(publisher_player_id, '[^A-Za-z0-9]+'::text, ''::text, 'g'::text))) WHERE (publisher_player_id IS NOT NULL);
CREATE INDEX game_accounts_user_game_idx ON game_accounts USING btree (user_id, game_id);

-- player_game_ratings

CREATE TABLE player_game_ratings (
    user_id uuid NOT NULL,
    game_id text NOT NULL,
    rating integer DEFAULT 1500 NOT NULL,
    matches_played integer DEFAULT 0 NOT NULL,
    wins integer DEFAULT 0 NOT NULL,
    draws integer DEFAULT 0 NOT NULL,
    losses integer DEFAULT 0 NOT NULL,
    rating_version bigint NOT NULL,
    last_match_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT player_game_ratings_check CHECK ((matches_played = ((wins + draws) + losses))),
    CONSTRAINT player_game_ratings_draws_check CHECK ((draws >= 0)),
    CONSTRAINT player_game_ratings_losses_check CHECK ((losses >= 0)),
    CONSTRAINT player_game_ratings_matches_played_check CHECK ((matches_played >= 0)),
    CONSTRAINT player_game_ratings_rating_check CHECK (((rating >= 0) AND (rating <= 10000))),
    CONSTRAINT player_game_ratings_rating_version_check CHECK ((rating_version >= 0)),
    CONSTRAINT player_game_ratings_wins_check CHECK ((wins >= 0))
);
ALTER TABLE ONLY player_game_ratings ALTER COLUMN rating_version SET DEFAULT nextval('player_game_rating_change_seq'::regclass);
ALTER TABLE ONLY player_game_ratings
    ADD CONSTRAINT player_game_ratings_pkey PRIMARY KEY (user_id, game_id);
CREATE INDEX player_game_ratings_leaderboard_idx ON player_game_ratings USING btree (game_id, rating DESC, wins DESC, user_id) WHERE (matches_played > 0);

-- leaderboard_snapshots

CREATE TABLE leaderboard_snapshots (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    game_id text NOT NULL,
    scope text NOT NULL,
    country_code character(2),
    status text DEFAULT 'building'::text NOT NULL,
    source_version bigint DEFAULT 0 NOT NULL,
    snapshot_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT leaderboard_snapshots_check CHECK ((((scope = 'global'::text) AND (country_code IS NULL)) OR ((scope = 'country'::text) AND (country_code IS NOT NULL)))),
    CONSTRAINT leaderboard_snapshots_scope_check CHECK ((scope = ANY (ARRAY['global'::text, 'country'::text]))),
    CONSTRAINT leaderboard_snapshots_source_version_check CHECK ((source_version >= 0)),
    CONSTRAINT leaderboard_snapshots_status_check CHECK ((status = ANY (ARRAY['building'::text, 'ready'::text])))
);
ALTER TABLE ONLY leaderboard_snapshots
    ADD CONSTRAINT leaderboard_snapshots_pkey PRIMARY KEY (id);
CREATE INDEX leaderboard_snapshots_latest_idx ON leaderboard_snapshots USING btree (game_id, scope, country_code, snapshot_at DESC, id DESC) WHERE (status = 'ready'::text);
CREATE INDEX leaderboard_snapshots_retention_idx ON leaderboard_snapshots USING btree (created_at, id);

-- leaderboard_snapshot_rows

CREATE TABLE leaderboard_snapshot_rows (
    snapshot_id uuid NOT NULL,
    rank integer NOT NULL,
    user_id uuid NOT NULL,
    rating integer NOT NULL,
    matches_played integer NOT NULL,
    wins integer NOT NULL,
    draws integer NOT NULL,
    losses integer NOT NULL,
    rank_movement integer DEFAULT 0 NOT NULL,
    CONSTRAINT leaderboard_snapshot_rows_check CHECK ((matches_played = ((wins + draws) + losses))),
    CONSTRAINT leaderboard_snapshot_rows_draws_check CHECK ((draws >= 0)),
    CONSTRAINT leaderboard_snapshot_rows_losses_check CHECK ((losses >= 0)),
    CONSTRAINT leaderboard_snapshot_rows_matches_played_check CHECK ((matches_played > 0)),
    CONSTRAINT leaderboard_snapshot_rows_rank_check CHECK ((rank > 0)),
    CONSTRAINT leaderboard_snapshot_rows_rating_check CHECK (((rating >= 0) AND (rating <= 10000))),
    CONSTRAINT leaderboard_snapshot_rows_wins_check CHECK ((wins >= 0))
);
ALTER TABLE ONLY leaderboard_snapshot_rows
    ADD CONSTRAINT leaderboard_snapshot_rows_pkey PRIMARY KEY (snapshot_id, rank);
ALTER TABLE ONLY leaderboard_snapshot_rows
    ADD CONSTRAINT leaderboard_snapshot_rows_snapshot_id_user_id_key UNIQUE (snapshot_id, user_id);
CREATE INDEX leaderboard_snapshot_rows_player_idx ON leaderboard_snapshot_rows USING btree (user_id, snapshot_id);

ALTER SEQUENCE player_game_rating_change_seq OWNED BY player_game_ratings.rating_version;

-- Relationships

ALTER TABLE ONLY game_accounts
    ADD CONSTRAINT game_accounts_game_id_fkey FOREIGN KEY (game_id) REFERENCES games(id);
ALTER TABLE ONLY game_accounts
    ADD CONSTRAINT game_accounts_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);
ALTER TABLE ONLY leaderboard_snapshot_rows
    ADD CONSTRAINT leaderboard_snapshot_rows_snapshot_id_fkey FOREIGN KEY (snapshot_id) REFERENCES leaderboard_snapshots(id) ON DELETE CASCADE;
ALTER TABLE ONLY leaderboard_snapshot_rows
    ADD CONSTRAINT leaderboard_snapshot_rows_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE ONLY leaderboard_snapshots
    ADD CONSTRAINT leaderboard_snapshots_game_id_fkey FOREIGN KEY (game_id) REFERENCES games(id);
ALTER TABLE ONLY player_game_ratings
    ADD CONSTRAINT player_game_ratings_game_id_fkey FOREIGN KEY (game_id) REFERENCES games(id);
ALTER TABLE ONLY player_game_ratings
    ADD CONSTRAINT player_game_ratings_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

-- Seed data

INSERT INTO games (id, name, publisher, supported_platforms, result_mode)
VALUES ('efootball-mobile', 'eFootball Mobile', 'Konami Digital Entertainment', ARRAY['android', 'ios'], 'participant_confirmation');

COMMIT;
