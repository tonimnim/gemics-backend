BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text,
    phone_e164 text,
    display_name text NOT NULL,
    country_code char(2) NOT NULL DEFAULT 'KE',
    -- Set when the player picks a country themselves. Until then the country
    -- follows the calling code of the phone number they save.
    country_chosen_at timestamptz,
    birth_date date,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('pending', 'active', 'suspended', 'deleted')),
    -- Players register with a username, Konami ID and password. Email and
    -- phone are optional contact details added after registration; email is
    -- stored only once its code is verified.
    password_hash text CHECK (password_hash IS NULL OR password_hash LIKE '$argon2id$%'),
    password_changed_at timestamptz,
    email_verified_at timestamptz,
    registration_ip text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- International E.164 with a leading plus, never one country's format:
    -- Kenya and India launch first, but the platform is global.
    CONSTRAINT users_phone_e164_chk CHECK (phone_e164 IS NULL OR phone_e164 ~ '^\+[1-9][0-9]{7,14}$')
);
CREATE UNIQUE INDEX users_email_unique ON users (lower(email)) WHERE email IS NOT NULL;
-- One account per phone: the anti-fraud handle for payments.
CREATE UNIQUE INDEX users_phone_unique ON users (phone_e164) WHERE phone_e164 IS NOT NULL;
-- Registrations per IP are counted here when Redis is unavailable.
CREATE INDEX users_registration_ip_idx ON users (registration_ip, created_at DESC)
    WHERE registration_ip IS NOT NULL;

CREATE TABLE player_profiles (
    user_id uuid PRIMARY KEY REFERENCES users(id),
    handle text NOT NULL,
    bio text NOT NULL DEFAULT '',
    avatar_object_key text,
    discoverable boolean NOT NULL DEFAULT false,
    analytics_consent_at timestamptz,
    scouting_consent_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX player_profiles_handle_unique ON player_profiles (lower(handle));

CREATE TABLE organizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    slug text NOT NULL,
    country_code char(2) NOT NULL DEFAULT 'KE',
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('pending', 'active', 'suspended')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX organizations_slug_unique ON organizations (lower(slug));

CREATE TABLE organization_members (
    organization_id uuid NOT NULL REFERENCES organizations(id),
    user_id uuid NOT NULL REFERENCES users(id),
    role text NOT NULL CHECK (role IN ('owner', 'admin', 'referee', 'analyst')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, user_id)
);

CREATE TABLE games (
    id text PRIMARY KEY,
    name text NOT NULL,
    publisher text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    supported_platforms text[] NOT NULL,
    result_mode text NOT NULL CHECK (result_mode IN ('participant_confirmation', 'publisher_api', 'admin_only')),
    rules_schema jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO games (id, name, publisher, supported_platforms, result_mode)
VALUES ('efootball-mobile', 'eFootball Mobile', 'Konami Digital Entertainment', ARRAY['android', 'ios'], 'participant_confirmation');

CREATE TABLE game_accounts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    game_id text NOT NULL REFERENCES games(id),
    -- Registration does not ask for a platform; '' means not chosen yet.
    platform text NOT NULL DEFAULT '',
    in_game_name text NOT NULL,
    publisher_player_id text CHECK (publisher_player_id IS NULL OR char_length(publisher_player_id) <= 64),
    verification_status text NOT NULL DEFAULT 'unverified' CHECK (verification_status IN ('unverified', 'pending', 'verified', 'rejected')),
    verified_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX game_accounts_user_game_idx ON game_accounts (user_id, game_id);
-- For eFootball the publisher ID is the player's Konami ID, which is also the
-- sign-in identifier. Players type it with varying case and separators, so
-- uniqueness and lookup use its uppercase alphanumeric form.
CREATE UNIQUE INDEX game_accounts_publisher_id_unique ON game_accounts
    (game_id, upper(regexp_replace(publisher_player_id, '[^A-Za-z0-9]+', '', 'g')))
    WHERE publisher_player_id IS NOT NULL;

CREATE TABLE competitions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    game_id text NOT NULL REFERENCES games(id),
    name text NOT NULL,
    slug text NOT NULL,
    description text NOT NULL DEFAULT '',
    format text NOT NULL CHECK (format IN ('single_elimination', 'double_elimination', 'round_robin')),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'registration_open', 'check_in', 'running', 'completed', 'cancelled')),
    rules_version integer NOT NULL DEFAULT 1 CHECK (rules_version > 0),
    rules_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    max_entries integer NOT NULL CHECK (max_entries >= 2),
    entry_fee_minor bigint NOT NULL DEFAULT 0 CHECK (entry_fee_minor >= 0),
    fee_purpose text NOT NULL DEFAULT 'none' CHECK (fee_purpose IN ('none', 'administration')),
    currency char(3) NOT NULL DEFAULT 'KES',
    prize_amount_minor bigint NOT NULL DEFAULT 0 CHECK (prize_amount_minor >= 0),
    prize_funding text NOT NULL DEFAULT 'none' CHECK (prize_funding IN ('none', 'organizer', 'sponsor')),
    registration_opens_at timestamptz NOT NULL,
    registration_closes_at timestamptz NOT NULL,
    check_in_opens_at timestamptz,
    starts_at timestamptz NOT NULL,
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (registration_opens_at < registration_closes_at),
    CHECK (registration_closes_at <= starts_at),
    CHECK ((entry_fee_minor = 0 AND fee_purpose = 'none') OR (entry_fee_minor > 0 AND fee_purpose = 'administration'))
);
CREATE UNIQUE INDEX competitions_org_slug_unique ON competitions (organization_id, lower(slug));
CREATE INDEX competitions_discovery_idx ON competitions (game_id, status, starts_at);

-- An entry is a competitor. It has one member for eFootball Mobile today and
-- can have several members for team games without changing bracket tables.
CREATE TABLE competition_entries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    competition_id uuid NOT NULL REFERENCES competitions(id),
    display_name text NOT NULL,
    captain_user_id uuid NOT NULL REFERENCES users(id),
    seed integer,
    status text NOT NULL DEFAULT 'registered' CHECK (status IN ('registered', 'checked_in', 'accepted', 'withdrawn', 'disqualified')),
    checked_in_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (competition_id, captain_user_id)
);
CREATE INDEX competition_entries_bracket_idx ON competition_entries (competition_id, status, seed);

CREATE TABLE entry_members (
    entry_id uuid NOT NULL REFERENCES competition_entries(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id),
    game_account_id uuid NOT NULL REFERENCES game_accounts(id),
    roster_role text NOT NULL DEFAULT 'starter' CHECK (roster_role IN ('starter', 'substitute', 'coach')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (entry_id, user_id),
    UNIQUE (entry_id, game_account_id)
);

CREATE TABLE competition_stages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    competition_id uuid NOT NULL REFERENCES competitions(id),
    position integer NOT NULL CHECK (position >= 0),
    name text NOT NULL,
    format text NOT NULL CHECK (format IN ('single_elimination', 'double_elimination', 'round_robin')),
    best_of smallint NOT NULL DEFAULT 1 CHECK (best_of > 0 AND best_of % 2 = 1),
    config jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'completed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (competition_id, position)
);

CREATE TABLE matches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    competition_id uuid NOT NULL REFERENCES competitions(id),
    stage_id uuid NOT NULL REFERENCES competition_stages(id),
    bracket text NOT NULL DEFAULT 'main',
    round_number integer NOT NULL,
    match_number integer NOT NULL,
    home_entry_id uuid REFERENCES competition_entries(id),
    away_entry_id uuid REFERENCES competition_entries(id),
    winner_entry_id uuid REFERENCES competition_entries(id),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'ready', 'in_progress', 'awaiting_confirmation', 'disputed', 'completed', 'forfeit', 'cancelled')),
    scheduled_at timestamptz,
    result_due_at timestamptz,
    completed_at timestamptz,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (home_entry_id IS NULL OR home_entry_id <> away_entry_id),
    UNIQUE (stage_id, bracket, round_number, match_number)
);
CREATE INDEX matches_entry_home_idx ON matches (home_entry_id, state);
CREATE INDEX matches_entry_away_idx ON matches (away_entry_id, state);
CREATE INDEX matches_operations_idx ON matches (competition_id, state, result_due_at);

CREATE TABLE result_submissions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    match_id uuid NOT NULL REFERENCES matches(id),
    submitted_by uuid NOT NULL REFERENCES users(id),
    home_score integer NOT NULL CHECK (home_score >= 0),
    away_score integer NOT NULL CHECK (away_score >= 0),
    game_results jsonb NOT NULL DEFAULT '[]'::jsonb,
    evidence_objects jsonb NOT NULL DEFAULT '[]'::jsonb,
    status text NOT NULL DEFAULT 'pending_confirmation' CHECK (status IN ('pending_confirmation', 'confirmed', 'disputed', 'rejected', 'superseded')),
    supersedes_id uuid REFERENCES result_submissions(id),
    submitted_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    decided_by uuid REFERENCES users(id)
);
CREATE INDEX result_submissions_match_idx ON result_submissions (match_id, submitted_at DESC);

CREATE TABLE result_confirmations (
    submission_id uuid NOT NULL REFERENCES result_submissions(id),
    user_id uuid NOT NULL REFERENCES users(id),
    decision text NOT NULL CHECK (decision IN ('confirm', 'dispute')),
    note text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (submission_id, user_id)
);

CREATE TABLE disputes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    match_id uuid NOT NULL REFERENCES matches(id),
    submission_id uuid REFERENCES result_submissions(id),
    opened_by uuid NOT NULL REFERENCES users(id),
    assigned_to uuid REFERENCES users(id),
    reason_code text NOT NULL,
    description text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'under_review', 'resolved', 'dismissed')),
    resolution text,
    opened_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz
);
CREATE INDEX disputes_queue_idx ON disputes (status, opened_at);

CREATE TABLE idempotency_keys (
    scope text NOT NULL,
    key text NOT NULL,
    request_hash text NOT NULL,
    response_status integer,
    response_body jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (scope, key)
);
CREATE INDEX idempotency_expiry_idx ON idempotency_keys (expires_at);

CREATE TABLE audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id uuid REFERENCES organizations(id),
    actor_user_id uuid REFERENCES users(id),
    action text NOT NULL,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    request_id text,
    before_state jsonb,
    after_state jsonb,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_subject_idx ON audit_events (subject_type, subject_id, occurred_at DESC);
CREATE INDEX audit_org_idx ON audit_events (organization_id, occurred_at DESC);

-- Transactions write domain changes and their outbox event atomically. Workers
-- deliver at least once, so every consumer must be idempotent by event id.
CREATE TABLE outbox_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),
    attempts integer NOT NULL DEFAULT 0,
    processed_at timestamptz,
    last_error text
);
CREATE INDEX outbox_delivery_idx ON outbox_events (available_at, occurred_at) WHERE processed_at IS NULL;

COMMIT;
