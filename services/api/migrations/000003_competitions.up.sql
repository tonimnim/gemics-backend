BEGIN;

-- Organizations, competitions, entries, draws, brackets, matches and progression.

-- Functions

CREATE FUNCTION stamp_competition_published_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.published_at IS NULL AND NEW.status NOT IN ('draft', 'cancelled') THEN
        NEW.published_at := now();
    END IF;
    RETURN NEW;
END;
$$;

SET default_tablespace = '';

SET default_table_access_method = heap;

-- currencies

-- Every currency Tonits can price a competition in. minor_unit is the ISO 4217
-- exponent: amounts are stored as integers of 10^-minor_unit (cents for KES,
-- whole yen for JPY).
CREATE TABLE currencies (
    code character(3) NOT NULL,
    minor_unit smallint NOT NULL,
    name text NOT NULL,
    CONSTRAINT currencies_code_check CHECK ((code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT currencies_minor_unit_check CHECK (((minor_unit >= 0) AND (minor_unit <= 3)))
);
ALTER TABLE ONLY currencies
    ADD CONSTRAINT currencies_pkey PRIMARY KEY (code);
INSERT INTO currencies (code, minor_unit, name) VALUES
    ('USD', 2, 'US dollar'),
    ('KES', 2, 'Kenyan shilling'),
    ('INR', 2, 'Indian rupee'),
    ('SGD', 2, 'Singapore dollar'),
    ('IDR', 2, 'Indonesian rupiah'),
    ('BRL', 2, 'Brazilian real'),
    ('JPY', 0, 'Japanese yen'),
    ('THB', 2, 'Thai baht'),
    ('MYR', 2, 'Malaysian ringgit'),
    ('UGX', 0, 'Ugandan shilling'),
    ('TZS', 2, 'Tanzanian shilling'),
    ('NGN', 2, 'Nigerian naira');

-- organizations

CREATE TABLE organizations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    country_code character(2) DEFAULT 'KE'::bpchar NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT organizations_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'active'::text, 'suspended'::text])))
);
ALTER TABLE ONLY organizations
    ADD CONSTRAINT organizations_pkey PRIMARY KEY (id);
CREATE INDEX organizations_active_name_trgm_idx ON organizations USING gin (lower(name) gin_trgm_ops) WHERE (status = 'active'::text);
CREATE INDEX organizations_admin_status_idx ON organizations USING btree (status, created_at DESC, id);
CREATE UNIQUE INDEX organizations_slug_unique ON organizations USING btree (lower(slug));

-- organization_members

CREATE TABLE organization_members (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT organization_members_role_v2_chk CHECK ((role = ANY (ARRAY['owner'::text, 'admin'::text, 'analyst'::text])))
);
ALTER TABLE ONLY organization_members
    ADD CONSTRAINT organization_members_pkey PRIMARY KEY (organization_id, user_id);
CREATE INDEX organization_members_user_idx ON organization_members USING btree (user_id, organization_id) INCLUDE (role);

-- competitions

CREATE TABLE competitions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    organization_id uuid NOT NULL,
    game_id text NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    format text NOT NULL,
    status text DEFAULT 'draft'::text NOT NULL,
    rules_version integer DEFAULT 1 NOT NULL,
    rules_snapshot jsonb DEFAULT '{}'::jsonb NOT NULL,
    max_entries integer NOT NULL,
    entry_fee_minor bigint DEFAULT 0 NOT NULL,
    fee_purpose text DEFAULT 'none'::text NOT NULL,
    currency character(3) DEFAULT 'KES'::bpchar NOT NULL,
    prize_amount_minor bigint DEFAULT 0 NOT NULL,
    prize_funding text DEFAULT 'none'::text NOT NULL,
    registration_opens_at timestamp with time zone NOT NULL,
    registration_closes_at timestamp with time zone NOT NULL,
    check_in_opens_at timestamp with time zone,
    starts_at timestamp with time zone NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    published_at timestamp with time zone,
    CONSTRAINT competitions_check CHECK ((registration_opens_at < registration_closes_at)),
    CONSTRAINT competitions_check1 CHECK ((registration_closes_at <= starts_at)),
    CONSTRAINT competitions_check2 CHECK ((((entry_fee_minor = 0) AND (fee_purpose = 'none'::text)) OR ((entry_fee_minor > 0) AND (fee_purpose = 'administration'::text)))),
    CONSTRAINT competitions_entry_fee_minor_check CHECK ((entry_fee_minor >= 0)),
    CONSTRAINT competitions_fee_purpose_check CHECK ((fee_purpose = ANY (ARRAY['none'::text, 'administration'::text]))),
    CONSTRAINT competitions_format_check CHECK ((format = ANY (ARRAY['single_elimination'::text, 'double_elimination'::text, 'round_robin'::text]))),
    CONSTRAINT competitions_max_entries_check CHECK ((max_entries >= 2)),
    CONSTRAINT competitions_prize_amount_minor_check CHECK ((prize_amount_minor >= 0)),
    CONSTRAINT competitions_prize_funding_check CHECK ((prize_funding = ANY (ARRAY['none'::text, 'organizer'::text, 'sponsor'::text]))),
    CONSTRAINT competitions_rules_version_check CHECK ((rules_version > 0)),
    CONSTRAINT competitions_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'published'::text, 'registration_open'::text, 'check_in'::text, 'running'::text, 'completed'::text, 'cancelled'::text])))
);
ALTER TABLE ONLY competitions
    ADD CONSTRAINT competitions_pkey PRIMARY KEY (id);
CREATE INDEX competitions_discovery_idx ON competitions USING btree (game_id, status, starts_at);
CREATE UNIQUE INDEX competitions_org_slug_unique ON competitions USING btree (organization_id, lower(slug));
CREATE INDEX competitions_org_status_starts_idx ON competitions USING btree (organization_id, status, starts_at DESC, id);
CREATE INDEX competitions_public_name_trgm_idx ON competitions USING gin (lower(name) gin_trgm_ops) WHERE (status = ANY (ARRAY['published'::text, 'registration_open'::text, 'check_in'::text, 'running'::text, 'completed'::text]));
CREATE INDEX competitions_public_starts_idx ON competitions USING btree (starts_at, id) WHERE (status = ANY (ARRAY['published'::text, 'registration_open'::text, 'check_in'::text, 'running'::text]));

-- competition_stages

CREATE TABLE competition_stages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    competition_id uuid NOT NULL,
    "position" integer NOT NULL,
    name text NOT NULL,
    format text NOT NULL,
    best_of smallint DEFAULT 1 NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT competition_stages_best_of_check CHECK (((best_of > 0) AND (((best_of)::integer % 2) = 1))),
    CONSTRAINT competition_stages_format_check CHECK ((format = ANY (ARRAY['single_elimination'::text, 'double_elimination'::text, 'round_robin'::text]))),
    CONSTRAINT competition_stages_position_check CHECK (("position" >= 0)),
    CONSTRAINT competition_stages_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'active'::text, 'completed'::text])))
);
ALTER TABLE ONLY competition_stages
    ADD CONSTRAINT competition_stages_competition_id_position_key UNIQUE (competition_id, "position");
ALTER TABLE ONLY competition_stages
    ADD CONSTRAINT competition_stages_pkey PRIMARY KEY (id);
CREATE UNIQUE INDEX competition_stages_id_competition_uidx ON competition_stages USING btree (id, competition_id);

-- competition_entries

CREATE TABLE competition_entries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    competition_id uuid NOT NULL,
    display_name text NOT NULL,
    captain_user_id uuid NOT NULL,
    seed integer,
    status text DEFAULT 'registered'::text NOT NULL,
    checked_in_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT competition_entries_seed_positive_chk CHECK (((seed IS NULL) OR (seed > 0))),
    CONSTRAINT competition_entries_status_check CHECK ((status = ANY (ARRAY['registered'::text, 'checked_in'::text, 'accepted'::text, 'withdrawal_pending'::text, 'withdrawn'::text, 'disqualified'::text])))
);
ALTER TABLE ONLY competition_entries
    ADD CONSTRAINT competition_entries_competition_id_captain_user_id_key UNIQUE (competition_id, captain_user_id);
ALTER TABLE ONLY competition_entries
    ADD CONSTRAINT competition_entries_pkey PRIMARY KEY (id);
CREATE INDEX competition_entries_bracket_idx ON competition_entries USING btree (competition_id, status, seed);
CREATE INDEX competition_entries_captain_created_idx ON competition_entries USING btree (captain_user_id, created_at DESC, id);
CREATE INDEX competition_entries_comp_status_created_idx ON competition_entries USING btree (competition_id, status, created_at DESC, id);
CREATE UNIQUE INDEX competition_entries_id_competition_uidx ON competition_entries USING btree (id, competition_id);
CREATE UNIQUE INDEX competition_entries_seed_uidx ON competition_entries USING btree (competition_id, seed) WHERE (seed IS NOT NULL);

-- entry_members

CREATE TABLE entry_members (
    entry_id uuid NOT NULL,
    user_id uuid NOT NULL,
    game_account_id uuid NOT NULL,
    roster_role text DEFAULT 'starter'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    competition_id uuid NOT NULL,
    CONSTRAINT entry_members_roster_role_check CHECK ((roster_role = ANY (ARRAY['starter'::text, 'substitute'::text, 'coach'::text])))
);
ALTER TABLE ONLY entry_members
    ADD CONSTRAINT entry_members_entry_id_game_account_id_key UNIQUE (entry_id, game_account_id);
ALTER TABLE ONLY entry_members
    ADD CONSTRAINT entry_members_pkey PRIMARY KEY (entry_id, user_id);
CREATE UNIQUE INDEX entry_members_one_player_per_competition_uidx ON entry_members USING btree (competition_id, user_id) WHERE (roster_role = ANY (ARRAY['starter'::text, 'substitute'::text]));
CREATE INDEX entry_members_user_entry_idx ON entry_members USING btree (user_id, entry_id) WHERE (roster_role = ANY (ARRAY['starter'::text, 'substitute'::text]));
CREATE INDEX entry_members_user_history_idx ON entry_members USING btree (user_id, competition_id, entry_id) WHERE (roster_role = ANY (ARRAY['starter'::text, 'substitute'::text]));

-- competition_draws

CREATE TABLE competition_draws (
    competition_id uuid NOT NULL,
    stage_id uuid NOT NULL,
    algorithm text NOT NULL,
    algorithm_version integer NOT NULL,
    draw_seed bytea NOT NULL,
    seeding_policy text NOT NULL,
    entry_count integer NOT NULL,
    entry_fingerprint bytea NOT NULL,
    graph_fingerprint bytea NOT NULL,
    match_count integer NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    generated_by uuid NOT NULL,
    generated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT competition_draws_algorithm_version_check CHECK ((algorithm_version > 0)),
    CONSTRAINT competition_draws_draw_seed_check CHECK ((octet_length(draw_seed) = 32)),
    CONSTRAINT competition_draws_entry_count_check CHECK ((entry_count >= 2)),
    CONSTRAINT competition_draws_entry_fingerprint_check CHECK ((octet_length(entry_fingerprint) = 32)),
    CONSTRAINT competition_draws_graph_fingerprint_check CHECK ((octet_length(graph_fingerprint) = 32)),
    CONSTRAINT competition_draws_match_count_check CHECK ((match_count > 0)),
    CONSTRAINT competition_draws_seeding_policy_check CHECK ((seeding_policy = ANY (ARRAY['seeded'::text, 'rating'::text, 'random'::text, 'registration_order'::text])))
);
ALTER TABLE ONLY competition_draws
    ADD CONSTRAINT competition_draws_pkey PRIMARY KEY (competition_id);

-- competition_draw_entries

CREATE TABLE competition_draw_entries (
    competition_id uuid NOT NULL,
    draw_position integer NOT NULL,
    entry_id uuid NOT NULL,
    group_key text,
    seed_key integer NOT NULL,
    rating_snapshot integer,
    deviation_snapshot integer,
    CONSTRAINT competition_draw_entries_draw_position_check CHECK ((draw_position > 0))
);
ALTER TABLE ONLY competition_draw_entries
    ADD CONSTRAINT competition_draw_entries_competition_id_entry_id_key UNIQUE (competition_id, entry_id);
ALTER TABLE ONLY competition_draw_entries
    ADD CONSTRAINT competition_draw_entries_pkey PRIMARY KEY (competition_id, draw_position);

-- competition_entry_placements

CREATE TABLE competition_entry_placements (
    competition_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    placement integer NOT NULL,
    finalized_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT competition_entry_placements_placement_check CHECK ((placement > 0))
);
ALTER TABLE ONLY competition_entry_placements
    ADD CONSTRAINT competition_entry_placements_pkey PRIMARY KEY (competition_id, entry_id);
CREATE INDEX competition_entry_placements_finish_idx ON competition_entry_placements USING btree (competition_id, placement, entry_id);

-- matches

CREATE TABLE matches (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    competition_id uuid NOT NULL,
    stage_id uuid NOT NULL,
    bracket text DEFAULT 'main'::text NOT NULL,
    round_number integer NOT NULL,
    match_number integer NOT NULL,
    home_entry_id uuid,
    away_entry_id uuid,
    winner_entry_id uuid,
    state text DEFAULT 'pending'::text NOT NULL,
    scheduled_at timestamp with time zone,
    result_due_at timestamp with time zone,
    completed_at timestamp with time zone,
    version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    check_in_opens_at timestamp with time zone,
    check_in_closes_at timestamp with time zone,
    graph_rank integer NOT NULL,
    group_key text,
    activation_rule text DEFAULT 'unconditional'::text NOT NULL,
    activation_source_match_id uuid,
    completion_reason text,
    CONSTRAINT matches_activation_pair_chk CHECK ((((activation_rule = 'unconditional'::text) AND (activation_source_match_id IS NULL)) OR ((activation_rule <> 'unconditional'::text) AND (activation_source_match_id IS NOT NULL)))),
    CONSTRAINT matches_activation_rule_check CHECK ((activation_rule = ANY (ARRAY['unconditional'::text, 'if_source_away_wins'::text]))),
    CONSTRAINT matches_check CHECK (((home_entry_id IS NULL) OR (home_entry_id <> away_entry_id))),
    CONSTRAINT matches_check_in_window_pair_chk CHECK ((((check_in_opens_at IS NULL) AND (check_in_closes_at IS NULL)) OR ((check_in_opens_at IS NOT NULL) AND (check_in_closes_at IS NOT NULL) AND (check_in_opens_at < check_in_closes_at)))),
    CONSTRAINT matches_completion_reason_v2_chk CHECK ((completion_reason = ANY (ARRAY['played'::text, 'walkover'::text, 'double_no_show'::text, 'referee'::text, 'timeout_forfeit'::text, 'reset_not_required'::text, 'correction_voided'::text, 'response_timeout'::text, 'no_result_reported'::text, 'platform_review'::text, 'competition_cancelled'::text]))),
    CONSTRAINT matches_graph_rank_positive_chk CHECK ((graph_rank > 0)),
    CONSTRAINT matches_number_positive_chk CHECK ((match_number > 0)),
    CONSTRAINT matches_round_positive_chk CHECK ((round_number > 0)),
    CONSTRAINT matches_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'ready'::text, 'in_progress'::text, 'awaiting_confirmation'::text, 'disputed'::text, 'completed'::text, 'forfeit'::text, 'cancelled'::text]))),
    CONSTRAINT matches_version_positive_chk CHECK ((version > 0)),
    CONSTRAINT matches_winner_participant_chk CHECK (((winner_entry_id IS NULL) OR (NOT (winner_entry_id IS DISTINCT FROM home_entry_id)) OR (NOT (winner_entry_id IS DISTINCT FROM away_entry_id))))
)
WITH (fillfactor='85');
ALTER TABLE ONLY matches
    ADD CONSTRAINT matches_pkey PRIMARY KEY (id);
ALTER TABLE ONLY matches
    ADD CONSTRAINT matches_stage_id_bracket_round_number_match_number_key UNIQUE (stage_id, bracket, round_number, match_number);
CREATE INDEX matches_away_active_schedule_idx ON matches USING btree (away_entry_id, COALESCE(completed_at, scheduled_at, created_at), id) WHERE (state = ANY (ARRAY['pending'::text, 'ready'::text, 'in_progress'::text, 'awaiting_confirmation'::text, 'disputed'::text]));
CREATE INDEX matches_away_history_schedule_idx ON matches USING btree (away_entry_id, COALESCE(completed_at, scheduled_at, created_at), id) WHERE (state = ANY (ARRAY['completed'::text, 'forfeit'::text, 'cancelled'::text]));
CREATE INDEX matches_deadline_idx ON matches USING btree (result_due_at) WHERE ((state = ANY (ARRAY['ready'::text, 'in_progress'::text])) AND (result_due_at IS NOT NULL));
CREATE INDEX matches_entry_away_idx ON matches USING btree (away_entry_id, state);
CREATE INDEX matches_entry_home_idx ON matches USING btree (home_entry_id, state);
CREATE INDEX matches_home_active_schedule_idx ON matches USING btree (home_entry_id, COALESCE(completed_at, scheduled_at, created_at), id) WHERE (state = ANY (ARRAY['pending'::text, 'ready'::text, 'in_progress'::text, 'awaiting_confirmation'::text, 'disputed'::text]));
CREATE INDEX matches_home_history_schedule_idx ON matches USING btree (home_entry_id, COALESCE(completed_at, scheduled_at, created_at), id) WHERE (state = ANY (ARRAY['completed'::text, 'forfeit'::text, 'cancelled'::text]));
CREATE UNIQUE INDEX matches_id_competition_uidx ON matches USING btree (id, competition_id);
CREATE UNIQUE INDEX matches_id_rank_uidx ON matches USING btree (id, graph_rank);
CREATE INDEX matches_operations_idx ON matches USING btree (competition_id, state, result_due_at);
CREATE INDEX matches_public_history_idx ON matches USING btree (completed_at DESC, id DESC) WHERE (state = ANY (ARRAY['completed'::text, 'forfeit'::text]));
CREATE INDEX matches_ready_check_in_deadline_idx ON matches USING btree (check_in_closes_at, id) WHERE ((state = 'ready'::text) AND (check_in_closes_at IS NOT NULL));
CREATE INDEX matches_release_idx ON matches USING btree (stage_id, round_number) WHERE ((state = 'pending'::text) AND (check_in_opens_at IS NOT NULL));
CREATE INDEX matches_stage_graph_idx ON matches USING btree (stage_id, bracket, graph_rank, match_number);
CREATE INDEX matches_stage_open_idx ON matches USING btree (stage_id) WHERE (state <> ALL (ARRAY['completed'::text, 'forfeit'::text, 'cancelled'::text]));

-- competition_entry_removals

CREATE TABLE competition_entry_removals (
    entry_id uuid NOT NULL,
    competition_id uuid NOT NULL,
    match_id uuid NOT NULL,
    reason_code text NOT NULL,
    previous_status text NOT NULL,
    actor_kind text NOT NULL,
    actor_user_id uuid,
    removed_at timestamp with time zone NOT NULL,
    CONSTRAINT competition_entry_removals_actor_chk CHECK (((actor_kind = 'staff'::text) = (actor_user_id IS NOT NULL))),
    CONSTRAINT competition_entry_removals_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['worker'::text, 'staff'::text, 'system'::text]))),
    CONSTRAINT competition_entry_removals_previous_status_check CHECK ((previous_status = ANY (ARRAY['registered'::text, 'checked_in'::text, 'accepted'::text, 'withdrawal_pending'::text]))),
    CONSTRAINT competition_entry_removals_reason_code_check CHECK ((reason_code = ANY (ARRAY['response_timeout'::text, 'no_result_reported'::text, 'platform_review'::text])))
);
ALTER TABLE ONLY competition_entry_removals
    ADD CONSTRAINT competition_entry_removals_pkey PRIMARY KEY (entry_id);
CREATE INDEX competition_entry_removals_competition_idx ON competition_entry_removals USING btree (competition_id, removed_at, entry_id);

-- competition_standings

CREATE TABLE competition_standings (
    stage_id uuid NOT NULL,
    competition_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    group_key text DEFAULT 'main'::text NOT NULL,
    played integer DEFAULT 0 NOT NULL,
    wins integer DEFAULT 0 NOT NULL,
    draws integer DEFAULT 0 NOT NULL,
    losses integer DEFAULT 0 NOT NULL,
    walkovers integer DEFAULT 0 NOT NULL,
    goals_for integer DEFAULT 0 NOT NULL,
    goals_against integer DEFAULT 0 NOT NULL,
    points integer DEFAULT 0 NOT NULL,
    standings_version bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT competition_standings_check CHECK ((played = ((wins + draws) + losses))),
    CONSTRAINT competition_standings_draws_check CHECK ((draws >= 0)),
    CONSTRAINT competition_standings_goals_against_check CHECK ((goals_against >= 0)),
    CONSTRAINT competition_standings_goals_for_check CHECK ((goals_for >= 0)),
    CONSTRAINT competition_standings_losses_check CHECK ((losses >= 0)),
    CONSTRAINT competition_standings_played_check CHECK ((played >= 0)),
    CONSTRAINT competition_standings_standings_version_check CHECK ((standings_version >= 0)),
    CONSTRAINT competition_standings_walkovers_check CHECK ((walkovers >= 0)),
    CONSTRAINT competition_standings_wins_check CHECK ((wins >= 0))
);
ALTER TABLE ONLY competition_standings
    ADD CONSTRAINT competition_standings_pkey PRIMARY KEY (stage_id, entry_id);
CREATE INDEX competition_standings_table_idx ON competition_standings USING btree (stage_id, group_key, points DESC, ((goals_for - goals_against)) DESC, goals_for DESC, entry_id);

-- match_slots

CREATE TABLE match_slots (
    match_id uuid NOT NULL,
    competition_id uuid NOT NULL,
    slot text NOT NULL,
    match_rank integer NOT NULL,
    source_kind text NOT NULL,
    source_entry_id uuid,
    source_match_id uuid,
    source_rank integer,
    resolved_entry_id uuid,
    resolved_at timestamp with time zone,
    voided_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT match_slots_acyclic_chk CHECK (((source_rank IS NULL) OR (source_rank < match_rank))),
    CONSTRAINT match_slots_exclusive_chk CHECK ((NOT ((resolved_at IS NOT NULL) AND (voided_at IS NOT NULL)))),
    CONSTRAINT match_slots_match_rank_check CHECK ((match_rank > 0)),
    CONSTRAINT match_slots_not_self_chk CHECK (((source_match_id IS NULL) OR (source_match_id <> match_id))),
    CONSTRAINT match_slots_resolution_chk CHECK (((resolved_entry_id IS NULL) = (resolved_at IS NULL))),
    CONSTRAINT match_slots_shape_chk CHECK ((((source_kind = 'entry'::text) AND (source_entry_id IS NOT NULL) AND (source_match_id IS NULL) AND (source_rank IS NULL)) OR ((source_kind = ANY (ARRAY['winner_of'::text, 'loser_of'::text])) AND (source_entry_id IS NULL) AND (source_match_id IS NOT NULL) AND (source_rank IS NOT NULL)))),
    CONSTRAINT match_slots_slot_check CHECK ((slot = ANY (ARRAY['home'::text, 'away'::text]))),
    CONSTRAINT match_slots_source_kind_check CHECK ((source_kind = ANY (ARRAY['entry'::text, 'winner_of'::text, 'loser_of'::text])))
);
ALTER TABLE ONLY match_slots
    ADD CONSTRAINT match_slots_pkey PRIMARY KEY (match_id, slot);
CREATE INDEX match_slots_competition_idx ON match_slots USING btree (competition_id);
CREATE UNIQUE INDEX match_slots_one_consumer_uidx ON match_slots USING btree (source_match_id, source_kind) WHERE (source_match_id IS NOT NULL);
CREATE INDEX match_slots_open_source_idx ON match_slots USING btree (source_match_id) WHERE ((resolved_at IS NULL) AND (voided_at IS NULL));

-- match_check_ins

CREATE TABLE match_check_ins (
    match_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    checked_in_by uuid NOT NULL,
    match_version integer NOT NULL,
    idempotency_key text NOT NULL,
    checked_in_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT match_check_ins_idempotency_key_check CHECK (((char_length(idempotency_key) >= 8) AND (char_length(idempotency_key) <= 128))),
    CONSTRAINT match_check_ins_match_version_check CHECK ((match_version > 0))
);
ALTER TABLE ONLY match_check_ins
    ADD CONSTRAINT match_check_ins_checked_in_by_idempotency_key_key UNIQUE (checked_in_by, idempotency_key);
ALTER TABLE ONLY match_check_ins
    ADD CONSTRAINT match_check_ins_pkey PRIMARY KEY (match_id, entry_id);

-- match_progression_applications

CREATE TABLE match_progression_applications (
    source_match_id uuid NOT NULL,
    finalized_match_version integer NOT NULL,
    competition_id uuid NOT NULL,
    stage_id uuid NOT NULL,
    outcome_hash bytea NOT NULL,
    outcome jsonb NOT NULL,
    cause text NOT NULL,
    actor_user_id uuid,
    application_result jsonb DEFAULT '{}'::jsonb NOT NULL,
    applied_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT match_progression_applications_application_result_check CHECK ((jsonb_typeof(application_result) = 'object'::text)),
    CONSTRAINT match_progression_applications_cause_v2_chk CHECK ((cause = ANY (ARRAY['player_confirmation'::text, 'referee'::text, 'timeout_forfeit'::text, 'withdrawal'::text, 'disqualification'::text, 'admin_correction'::text, 'platform_review'::text]))),
    CONSTRAINT match_progression_applications_finalized_match_version_check CHECK ((finalized_match_version > 0)),
    CONSTRAINT match_progression_applications_outcome_check CHECK ((jsonb_typeof(outcome) = 'object'::text)),
    CONSTRAINT match_progression_applications_outcome_hash_check CHECK ((octet_length(outcome_hash) = 32))
);
ALTER TABLE ONLY match_progression_applications
    ADD CONSTRAINT match_progression_applications_pkey PRIMARY KEY (source_match_id, finalized_match_version);
CREATE INDEX match_progression_applications_competition_idx ON match_progression_applications USING btree (competition_id, applied_at DESC, source_match_id);
CREATE INDEX match_progression_applications_stage_idx ON match_progression_applications USING btree (stage_id, applied_at DESC, source_match_id);

-- progression_events

CREATE TABLE progression_events (
    id bigint NOT NULL,
    competition_id uuid NOT NULL,
    stage_id uuid NOT NULL,
    source_match_id uuid,
    target_match_id uuid,
    target_slot text,
    entry_id uuid,
    kind text NOT NULL,
    cause text NOT NULL,
    actor_user_id uuid,
    detail jsonb DEFAULT '{}'::jsonb NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT progression_events_cause_v2_chk CHECK ((cause = ANY (ARRAY['draw'::text, 'player_confirmation'::text, 'referee'::text, 'timeout_forfeit'::text, 'withdrawal'::text, 'disqualification'::text, 'admin_correction'::text, 'platform_review'::text]))),
    CONSTRAINT progression_events_kind_check CHECK ((kind = ANY (ARRAY['slot_filled'::text, 'slot_voided'::text, 'match_readied'::text, 'match_forfeited'::text, 'match_cancelled'::text, 'round_released'::text, 'stage_completed'::text, 'placements_written'::text, 'subgraph_unwound'::text, 'standings_updated'::text]))),
    CONSTRAINT progression_events_target_slot_check CHECK ((target_slot = ANY (ARRAY['home'::text, 'away'::text])))
);
ALTER TABLE progression_events ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME progression_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);
ALTER TABLE ONLY progression_events
    ADD CONSTRAINT progression_events_pkey PRIMARY KEY (id);
CREATE INDEX progression_events_competition_idx ON progression_events USING btree (competition_id, id DESC);

-- Relationships

ALTER TABLE ONLY competitions
    ADD CONSTRAINT competitions_currency_fkey FOREIGN KEY (currency) REFERENCES currencies(code);
ALTER TABLE ONLY competition_draw_entries
    ADD CONSTRAINT competition_draw_entries_competition_id_fkey FOREIGN KEY (competition_id) REFERENCES competition_draws(competition_id) ON DELETE CASCADE;
ALTER TABLE ONLY competition_draw_entries
    ADD CONSTRAINT competition_draw_entries_entry_id_competition_id_fkey FOREIGN KEY (entry_id, competition_id) REFERENCES competition_entries(id, competition_id);
ALTER TABLE ONLY competition_draws
    ADD CONSTRAINT competition_draws_competition_id_fkey FOREIGN KEY (competition_id) REFERENCES competitions(id) ON DELETE CASCADE;
ALTER TABLE ONLY competition_draws
    ADD CONSTRAINT competition_draws_generated_by_fkey FOREIGN KEY (generated_by) REFERENCES users(id);
ALTER TABLE ONLY competition_draws
    ADD CONSTRAINT competition_draws_stage_id_competition_id_fkey FOREIGN KEY (stage_id, competition_id) REFERENCES competition_stages(id, competition_id);
ALTER TABLE ONLY competition_entries
    ADD CONSTRAINT competition_entries_captain_user_id_fkey FOREIGN KEY (captain_user_id) REFERENCES users(id);
ALTER TABLE ONLY competition_entries
    ADD CONSTRAINT competition_entries_competition_id_fkey FOREIGN KEY (competition_id) REFERENCES competitions(id);
ALTER TABLE ONLY competition_entry_placements
    ADD CONSTRAINT competition_entry_placements_entry_id_competition_id_fkey FOREIGN KEY (entry_id, competition_id) REFERENCES competition_entries(id, competition_id) ON DELETE CASCADE;
ALTER TABLE ONLY competition_entry_removals
    ADD CONSTRAINT competition_entry_removals_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES users(id);
ALTER TABLE ONLY competition_entry_removals
    ADD CONSTRAINT competition_entry_removals_entry_id_competition_id_fkey FOREIGN KEY (entry_id, competition_id) REFERENCES competition_entries(id, competition_id);
ALTER TABLE ONLY competition_entry_removals
    ADD CONSTRAINT competition_entry_removals_match_id_competition_id_fkey FOREIGN KEY (match_id, competition_id) REFERENCES matches(id, competition_id);
ALTER TABLE ONLY competition_stages
    ADD CONSTRAINT competition_stages_competition_id_fkey FOREIGN KEY (competition_id) REFERENCES competitions(id);
ALTER TABLE ONLY competition_standings
    ADD CONSTRAINT competition_standings_entry_id_competition_id_fkey FOREIGN KEY (entry_id, competition_id) REFERENCES competition_entries(id, competition_id);
ALTER TABLE ONLY competition_standings
    ADD CONSTRAINT competition_standings_stage_id_competition_id_fkey FOREIGN KEY (stage_id, competition_id) REFERENCES competition_stages(id, competition_id);
ALTER TABLE ONLY competitions
    ADD CONSTRAINT competitions_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id);
ALTER TABLE ONLY competitions
    ADD CONSTRAINT competitions_game_id_fkey FOREIGN KEY (game_id) REFERENCES games(id);
ALTER TABLE ONLY competitions
    ADD CONSTRAINT competitions_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations(id);
ALTER TABLE ONLY entry_members
    ADD CONSTRAINT entry_members_entry_competition_fk FOREIGN KEY (entry_id, competition_id) REFERENCES competition_entries(id, competition_id);
ALTER TABLE ONLY entry_members
    ADD CONSTRAINT entry_members_entry_id_fkey FOREIGN KEY (entry_id) REFERENCES competition_entries(id) ON DELETE CASCADE;
ALTER TABLE ONLY entry_members
    ADD CONSTRAINT entry_members_game_account_id_fkey FOREIGN KEY (game_account_id) REFERENCES game_accounts(id);
ALTER TABLE ONLY entry_members
    ADD CONSTRAINT entry_members_game_account_owner_fk FOREIGN KEY (game_account_id, user_id) REFERENCES game_accounts(id, user_id);
ALTER TABLE ONLY entry_members
    ADD CONSTRAINT entry_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);
ALTER TABLE ONLY match_check_ins
    ADD CONSTRAINT match_check_ins_entry_id_fkey FOREIGN KEY (entry_id) REFERENCES competition_entries(id);
ALTER TABLE ONLY match_check_ins
    ADD CONSTRAINT match_check_ins_match_id_fkey FOREIGN KEY (match_id) REFERENCES matches(id) ON DELETE CASCADE;
ALTER TABLE ONLY match_check_ins
    ADD CONSTRAINT match_check_ins_roster_member_fk FOREIGN KEY (entry_id, checked_in_by) REFERENCES entry_members(entry_id, user_id);
ALTER TABLE ONLY match_progression_applications
    ADD CONSTRAINT match_progression_application_source_match_id_competition__fkey FOREIGN KEY (source_match_id, competition_id) REFERENCES matches(id, competition_id) ON DELETE CASCADE;
ALTER TABLE ONLY match_progression_applications
    ADD CONSTRAINT match_progression_applications_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES users(id);
ALTER TABLE ONLY match_progression_applications
    ADD CONSTRAINT match_progression_applications_stage_id_competition_id_fkey FOREIGN KEY (stage_id, competition_id) REFERENCES competition_stages(id, competition_id) ON DELETE CASCADE;
ALTER TABLE ONLY match_slots
    ADD CONSTRAINT match_slots_match_id_competition_id_fkey FOREIGN KEY (match_id, competition_id) REFERENCES matches(id, competition_id) ON DELETE CASCADE;
ALTER TABLE ONLY match_slots
    ADD CONSTRAINT match_slots_match_id_match_rank_fkey FOREIGN KEY (match_id, match_rank) REFERENCES matches(id, graph_rank);
ALTER TABLE ONLY match_slots
    ADD CONSTRAINT match_slots_resolved_entry_id_competition_id_fkey FOREIGN KEY (resolved_entry_id, competition_id) REFERENCES competition_entries(id, competition_id);
ALTER TABLE ONLY match_slots
    ADD CONSTRAINT match_slots_source_entry_id_competition_id_fkey FOREIGN KEY (source_entry_id, competition_id) REFERENCES competition_entries(id, competition_id);
ALTER TABLE ONLY match_slots
    ADD CONSTRAINT match_slots_source_match_id_competition_id_fkey FOREIGN KEY (source_match_id, competition_id) REFERENCES matches(id, competition_id);
ALTER TABLE ONLY match_slots
    ADD CONSTRAINT match_slots_source_match_id_source_rank_fkey FOREIGN KEY (source_match_id, source_rank) REFERENCES matches(id, graph_rank);
ALTER TABLE ONLY matches
    ADD CONSTRAINT matches_away_competition_fk FOREIGN KEY (away_entry_id, competition_id) REFERENCES competition_entries(id, competition_id);
ALTER TABLE ONLY matches
    ADD CONSTRAINT matches_away_entry_id_fkey FOREIGN KEY (away_entry_id) REFERENCES competition_entries(id);
ALTER TABLE ONLY matches
    ADD CONSTRAINT matches_competition_id_fkey FOREIGN KEY (competition_id) REFERENCES competitions(id);
ALTER TABLE ONLY matches
    ADD CONSTRAINT matches_home_competition_fk FOREIGN KEY (home_entry_id, competition_id) REFERENCES competition_entries(id, competition_id);
ALTER TABLE ONLY matches
    ADD CONSTRAINT matches_home_entry_id_fkey FOREIGN KEY (home_entry_id) REFERENCES competition_entries(id);
ALTER TABLE ONLY matches
    ADD CONSTRAINT matches_stage_competition_fk FOREIGN KEY (stage_id, competition_id) REFERENCES competition_stages(id, competition_id);
ALTER TABLE ONLY matches
    ADD CONSTRAINT matches_stage_id_fkey FOREIGN KEY (stage_id) REFERENCES competition_stages(id);
ALTER TABLE ONLY matches
    ADD CONSTRAINT matches_winner_competition_fk FOREIGN KEY (winner_entry_id, competition_id) REFERENCES competition_entries(id, competition_id);
ALTER TABLE ONLY matches
    ADD CONSTRAINT matches_winner_entry_id_fkey FOREIGN KEY (winner_entry_id) REFERENCES competition_entries(id);
ALTER TABLE ONLY organization_members
    ADD CONSTRAINT organization_members_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations(id);
ALTER TABLE ONLY organization_members
    ADD CONSTRAINT organization_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);
ALTER TABLE ONLY progression_events
    ADD CONSTRAINT progression_events_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES users(id);
ALTER TABLE ONLY progression_events
    ADD CONSTRAINT progression_events_competition_id_fkey FOREIGN KEY (competition_id) REFERENCES competitions(id) ON DELETE CASCADE;

-- Triggers

CREATE TRIGGER competitions_stamp_published_at BEFORE INSERT OR UPDATE OF status ON competitions FOR EACH ROW EXECUTE FUNCTION stamp_competition_published_at();

-- Seed data

-- Gamics runs every V1 competition itself. Staff holding the competition.manage
-- platform permission act as admins of this organization (gamicsOrganizationID).
INSERT INTO organizations (id, name, slug, country_code)
VALUES ('6a1c5e00-0000-4000-8000-000000000001', 'Gamics', 'gamics', 'KE');

COMMIT;
