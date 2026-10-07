BEGIN;

ALTER TABLE users
    ADD COLUMN terms_accepted_at timestamptz,
    ADD COLUMN privacy_accepted_at timestamptz;

-- Email codes never sign anyone in. They verify an address a signed-in
-- player added, or reset a forgotten password through a verified address.
CREATE TABLE email_otp_challenges (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    purpose text NOT NULL CHECK (purpose IN ('email_verification', 'password_reset')),
    email text NOT NULL,
    code_hash bytea NOT NULL,
    request_ip text NOT NULL,
    attempts smallint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_otp_challenges_lookup_idx
    ON email_otp_challenges (lower(email), created_at DESC);
CREATE INDEX email_otp_challenges_rate_idx
    ON email_otp_challenges (request_ip, created_at DESC);
CREATE INDEX email_otp_challenges_user_idx
    ON email_otp_challenges (user_id, purpose, created_at DESC);

-- Failed sign-ins, counted per Konami ID and per IP to slow password
-- guessing. The sign-in handler prunes rows older than a day.
CREATE TABLE login_failures (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    konami_key text NOT NULL,
    request_ip text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX login_failures_key_idx ON login_failures (konami_key, created_at DESC);
CREATE INDEX login_failures_ip_idx ON login_failures (request_ip, created_at DESC);
CREATE INDEX login_failures_created_idx ON login_failures (created_at);

CREATE TABLE refresh_sessions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    device_name text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    created_ip text NOT NULL DEFAULT '',
    last_used_ip text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);
CREATE INDEX refresh_sessions_user_active_idx
    ON refresh_sessions (user_id, last_used_at DESC)
    WHERE revoked_at IS NULL;

COMMIT;
