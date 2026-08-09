BEGIN;

ALTER TABLE users
    ADD COLUMN terms_accepted_at timestamptz,
    ADD COLUMN privacy_accepted_at timestamptz;

CREATE TABLE email_otp_challenges (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
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
