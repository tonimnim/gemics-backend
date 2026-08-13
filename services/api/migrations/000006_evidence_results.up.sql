BEGIN;

CREATE TABLE evidence_uploads (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    storage_provider text NOT NULL DEFAULT 's3' CHECK (storage_provider = 's3'),
    object_key text NOT NULL UNIQUE,
    media_type text NOT NULL CHECK (media_type IN ('image/jpeg', 'image/png', 'image/heic', 'image/heif')),
    byte_size bigint NOT NULL CHECK (byte_size > 0 AND byte_size <= 26214400),
    checksum_sha256 bytea NOT NULL CHECK (octet_length(checksum_sha256) = 32),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed', 'failed', 'expired')),
    upload_expires_at timestamptz NOT NULL,
    completed_at timestamptz,
    provider_etag text,
    bound_kind text CHECK (bound_kind IN ('result_submission', 'result_dispute')),
    bound_id uuid,
    bound_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((bound_kind IS NULL) = (bound_id IS NULL)),
    CHECK ((bound_id IS NULL) = (bound_at IS NULL)),
    CHECK (status <> 'completed' OR completed_at IS NOT NULL),
    CHECK (bound_id IS NULL OR status = 'completed')
);
CREATE INDEX evidence_uploads_owner_created_idx
    ON evidence_uploads (owner_user_id, created_at DESC, id);
CREATE INDEX evidence_uploads_pending_expiry_idx
    ON evidence_uploads (upload_expires_at, id) WHERE status = 'pending';
CREATE INDEX evidence_uploads_bound_idx
    ON evidence_uploads (bound_kind, bound_id) WHERE bound_id IS NOT NULL;

-- Older prelaunch submissions used the table default rather than a per-game
-- breakdown. Backfill them before validating the stronger array constraint.
UPDATE result_submissions
SET game_results = jsonb_build_array(jsonb_build_object(
    'homeScore', home_score,
    'awayScore', away_score
))
WHERE jsonb_typeof(game_results) = 'array'
  AND jsonb_array_length(game_results) = 0;

ALTER TABLE result_submissions
    ADD COLUMN match_version integer NOT NULL DEFAULT 1,
    ADD COLUMN tiebreak_type text,
    ADD COLUMN home_tiebreak_score integer,
    ADD COLUMN away_tiebreak_score integer,
    ADD CONSTRAINT result_submissions_match_version_positive_chk CHECK (match_version > 0) NOT VALID,
    ADD CONSTRAINT result_submissions_score_upper_bound_chk CHECK (home_score <= 99 AND away_score <= 99) NOT VALID,
    ADD CONSTRAINT result_submissions_tiebreak_chk CHECK (
        (tiebreak_type IS NULL AND home_tiebreak_score IS NULL AND away_tiebreak_score IS NULL)
        OR
        (tiebreak_type = 'penalties'
            AND home_score = away_score
            AND home_tiebreak_score IS NOT NULL
            AND away_tiebreak_score IS NOT NULL
            AND home_tiebreak_score BETWEEN 0 AND 99
            AND away_tiebreak_score BETWEEN 0 AND 99
            AND home_tiebreak_score <> away_tiebreak_score)
    ) NOT VALID,
    ADD CONSTRAINT result_submissions_games_array_chk CHECK (
        jsonb_typeof(game_results) = 'array'
        AND jsonb_array_length(game_results) BETWEEN 1 AND 99
    ) NOT VALID;

CREATE UNIQUE INDEX result_submissions_one_pending_per_match_uidx
    ON result_submissions (match_id) WHERE status = 'pending_confirmation';

CREATE TABLE result_submission_evidence (
    submission_id uuid NOT NULL REFERENCES result_submissions(id) ON DELETE CASCADE,
    evidence_id uuid NOT NULL UNIQUE REFERENCES evidence_uploads(id),
    position smallint NOT NULL CHECK (position >= 0),
    PRIMARY KEY (submission_id, evidence_id),
    UNIQUE (submission_id, position)
);

ALTER TABLE result_confirmations
    ADD COLUMN reason_code text,
    ADD CONSTRAINT result_confirmations_decision_payload_chk CHECK (
        (decision = 'confirm' AND reason_code IS NULL AND note = '')
        OR
        (decision = 'dispute'
            AND reason_code IN ('score_mismatch', 'invalid_evidence', 'match_not_played', 'other')
            AND length(note) BETWEEN 10 AND 1000)
    ) NOT VALID;

CREATE TABLE result_confirmation_evidence (
    submission_id uuid NOT NULL,
    user_id uuid NOT NULL,
    evidence_id uuid NOT NULL UNIQUE REFERENCES evidence_uploads(id),
    position smallint NOT NULL CHECK (position >= 0),
    PRIMARY KEY (submission_id, user_id, evidence_id),
    UNIQUE (submission_id, user_id, position),
    FOREIGN KEY (submission_id, user_id)
        REFERENCES result_confirmations (submission_id, user_id) ON DELETE CASCADE
);

ALTER TABLE result_submissions VALIDATE CONSTRAINT result_submissions_match_version_positive_chk;
ALTER TABLE result_submissions VALIDATE CONSTRAINT result_submissions_score_upper_bound_chk;
ALTER TABLE result_submissions VALIDATE CONSTRAINT result_submissions_tiebreak_chk;
ALTER TABLE result_submissions VALIDATE CONSTRAINT result_submissions_games_array_chk;
ALTER TABLE result_confirmations VALIDATE CONSTRAINT result_confirmations_decision_payload_chk;

COMMIT;
