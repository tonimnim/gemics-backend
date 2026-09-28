BEGIN;

-- Evidence uploads are checksum-verified at completion. Video duration cannot
-- be trusted from client metadata, so videos remain in `processing` until an
-- asynchronous media verifier records verified_duration_seconds.
ALTER TABLE evidence_uploads
    DROP CONSTRAINT evidence_uploads_media_type_check,
    DROP CONSTRAINT evidence_uploads_byte_size_check,
    DROP CONSTRAINT evidence_uploads_status_check,
    DROP CONSTRAINT evidence_uploads_bound_kind_check,
    ADD COLUMN media_kind text NOT NULL DEFAULT 'image',
    ADD COLUMN declared_duration_seconds numeric(8,3),
    ADD COLUMN verified_duration_seconds numeric(8,3),
    ADD COLUMN processing_error_code text,
    ADD COLUMN processed_at timestamptz,
    ADD CONSTRAINT evidence_uploads_media_type_v2_chk CHECK (
        media_type IN (
            'image/jpeg', 'image/png', 'image/heic', 'image/heif',
            'video/mp4', 'video/quicktime', 'video/webm'
        )
    ),
    ADD CONSTRAINT evidence_uploads_media_kind_chk CHECK (media_kind IN ('image', 'video')),
    ADD CONSTRAINT evidence_uploads_byte_size_v2_chk CHECK (byte_size > 0 AND byte_size <= 262144000),
    ADD CONSTRAINT evidence_uploads_status_v2_chk CHECK (
        status IN ('pending', 'processing', 'completed', 'failed', 'rejected', 'expired')
    ),
    ADD CONSTRAINT evidence_uploads_bound_kind_v2_chk CHECK (
        bound_kind IN (
            'result_submission', 'result_dispute', 'dispute_case',
            'game_account_verification'
        )
    ),
    ADD CONSTRAINT evidence_uploads_media_duration_chk CHECK (
        (media_kind = 'image'
            AND media_type LIKE 'image/%'
            AND declared_duration_seconds IS NULL
            AND verified_duration_seconds IS NULL)
        OR
        (media_kind = 'video'
            AND media_type LIKE 'video/%'
            AND declared_duration_seconds > 0
            AND declared_duration_seconds <= 120
            AND (verified_duration_seconds IS NULL
                OR verified_duration_seconds > 0))
    );

-- Existing image uploads were synchronously checksum-verified, so their old
-- completion timestamp is also a valid processing timestamp.
UPDATE evidence_uploads
SET processed_at = completed_at
WHERE status = 'completed' AND processed_at IS NULL;

ALTER TABLE evidence_uploads
    ADD CONSTRAINT evidence_uploads_processing_state_chk CHECK (
        (status = 'processing' AND media_kind = 'video' AND completed_at IS NOT NULL AND processed_at IS NULL)
        OR status <> 'processing'
    ),
    ADD CONSTRAINT evidence_uploads_processed_state_chk CHECK (
        (status IN ('completed', 'rejected') AND processed_at IS NOT NULL)
        OR status NOT IN ('completed', 'rejected')
    ),
    ADD CONSTRAINT evidence_uploads_verified_video_ready_chk CHECK (
        status <> 'completed' OR media_kind <> 'video'
        OR (verified_duration_seconds > 0 AND verified_duration_seconds <= 120)
    );

CREATE INDEX evidence_uploads_processing_idx
    ON evidence_uploads (created_at, id) WHERE status = 'processing';

-- Durable queue claimed by an isolated media worker. The worker inspects the
-- real container/codec/duration, then atomically updates both this row and the
-- evidence status. API requests never claim to have performed that inspection.
CREATE TABLE evidence_media_processing_jobs (
    evidence_id uuid PRIMARY KEY REFERENCES evidence_uploads(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'succeeded', 'rejected', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    locked_at timestamptz,
    locked_by text,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    CHECK ((locked_at IS NULL) = (locked_by IS NULL)),
    CHECK (status NOT IN ('succeeded', 'rejected', 'failed') OR finished_at IS NOT NULL)
);
CREATE INDEX evidence_media_processing_queue_idx
    ON evidence_media_processing_jobs (available_at, evidence_id)
    WHERE status IN ('queued', 'failed');

CREATE TABLE profile_media_uploads (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    object_key text NOT NULL UNIQUE,
    media_type text NOT NULL CHECK (media_type IN ('image/jpeg', 'image/png', 'image/webp')),
    byte_size bigint NOT NULL CHECK (byte_size > 0 AND byte_size <= 10485760),
    checksum_sha256 bytea NOT NULL CHECK (octet_length(checksum_sha256) = 32),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'completed', 'attached', 'failed', 'expired')),
    upload_expires_at timestamptz NOT NULL,
    completed_at timestamptz,
    attached_at timestamptz,
    provider_etag text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (status NOT IN ('completed', 'attached') OR completed_at IS NOT NULL),
    CHECK (status <> 'attached' OR attached_at IS NOT NULL)
);
CREATE INDEX profile_media_uploads_user_created_idx
    ON profile_media_uploads (user_id, created_at DESC, id);
CREATE INDEX profile_media_uploads_pending_expiry_idx
    ON profile_media_uploads (upload_expires_at, id) WHERE status = 'pending';

ALTER TABLE disputes
    ADD COLUMN decision_code text,
    ADD COLUMN decision_note text,
    ADD COLUMN decided_by uuid REFERENCES users(id),
    ADD COLUMN appeal_deadline timestamptz,
    ADD COLUMN appeal_status text NOT NULL DEFAULT 'none'
        CHECK (appeal_status IN ('none', 'available', 'pending', 'resolved', 'rejected', 'expired')),
    ADD COLUMN appeal_decided_at timestamptz,
    ADD CONSTRAINT disputes_decision_payload_chk CHECK (
        (decision_code IS NULL AND decision_note IS NULL AND decided_by IS NULL)
        OR
        (decision_code IS NOT NULL AND decision_note IS NOT NULL AND decided_by IS NOT NULL)
    ),
    ADD CONSTRAINT disputes_appeal_window_chk CHECK (
        (appeal_status = 'none' AND appeal_deadline IS NULL)
        OR (appeal_status <> 'none' AND appeal_deadline IS NOT NULL)
    );

CREATE TABLE dispute_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    dispute_id uuid NOT NULL REFERENCES disputes(id) ON DELETE CASCADE,
    actor_user_id uuid REFERENCES users(id),
    event_type text NOT NULL CHECK (event_type IN (
        'opened', 'status_changed', 'evidence_requested', 'evidence_submitted',
        'decision_recorded', 'appeal_submitted', 'appeal_decided'
    )),
    visible_to_participants boolean NOT NULL DEFAULT true,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX dispute_events_case_timeline_idx
    ON dispute_events (dispute_id, created_at, id) WHERE visible_to_participants;

INSERT INTO dispute_events (dispute_id, actor_user_id, event_type, metadata, created_at)
SELECT id, opened_by, 'opened',
    jsonb_build_object('reasonCode', reason_code), opened_at
FROM disputes;

INSERT INTO dispute_events (dispute_id, actor_user_id, event_type, metadata, created_at)
SELECT id, decided_by, 'decision_recorded',
    jsonb_build_object(
        'status', status,
        'decisionCode', COALESCE(decision_code, status),
        'note', COALESCE(decision_note, resolution, '')
    ), COALESCE(resolved_at, opened_at)
FROM disputes
WHERE status IN ('resolved', 'dismissed');

CREATE TABLE dispute_evidence_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    dispute_id uuid NOT NULL REFERENCES disputes(id) ON DELETE CASCADE,
    requested_from uuid NOT NULL REFERENCES users(id),
    requested_by uuid NOT NULL REFERENCES users(id),
    message text NOT NULL CHECK (length(message) BETWEEN 1 AND 1000),
    allowed_media_types text[] NOT NULL DEFAULT ARRAY['image/jpeg', 'image/png']::text[],
    max_items smallint NOT NULL DEFAULT 3 CHECK (max_items BETWEEN 1 AND 5),
    due_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'fulfilled', 'cancelled', 'expired')),
    requested_at timestamptz NOT NULL DEFAULT now(),
    responded_at timestamptz,
    UNIQUE (id, dispute_id),
    CHECK (cardinality(allowed_media_types) BETWEEN 1 AND 7),
    CHECK (allowed_media_types <@ ARRAY[
        'image/jpeg', 'image/png', 'image/heic', 'image/heif',
        'video/mp4', 'video/quicktime', 'video/webm'
    ]::text[]),
    CHECK (due_at > requested_at),
    CHECK ((status = 'fulfilled') = (responded_at IS NOT NULL))
);
CREATE INDEX dispute_evidence_requests_case_idx
    ON dispute_evidence_requests (dispute_id, requested_at, id);
CREATE INDEX dispute_evidence_requests_player_open_idx
    ON dispute_evidence_requests (requested_from, due_at, id) WHERE status = 'open';

CREATE TABLE dispute_evidence (
    dispute_id uuid NOT NULL REFERENCES disputes(id) ON DELETE CASCADE,
    request_id uuid,
    evidence_id uuid NOT NULL UNIQUE REFERENCES evidence_uploads(id),
    submitted_by uuid NOT NULL REFERENCES users(id),
    note text NOT NULL DEFAULT '' CHECK (length(note) <= 1000),
    submitted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dispute_id, evidence_id),
    FOREIGN KEY (request_id, dispute_id)
        REFERENCES dispute_evidence_requests (id, dispute_id) ON DELETE CASCADE
);
CREATE INDEX dispute_evidence_case_idx
    ON dispute_evidence (dispute_id, submitted_at, evidence_id);

CREATE TABLE dispute_appeals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    dispute_id uuid NOT NULL UNIQUE REFERENCES disputes(id) ON DELETE CASCADE,
    opened_by uuid NOT NULL REFERENCES users(id),
    reason text NOT NULL CHECK (length(reason) BETWEEN 20 AND 2000),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'rejected', 'withdrawn')),
    opened_at timestamptz NOT NULL DEFAULT now(),
    decided_by uuid REFERENCES users(id),
    decision_note text,
    decided_at timestamptz,
    CHECK ((decided_by IS NULL) = (decided_at IS NULL)),
    CHECK (decision_note IS NULL OR length(decision_note) BETWEEN 1 AND 2000)
);
CREATE INDEX dispute_appeals_queue_idx
    ON dispute_appeals (status, opened_at, id) WHERE status = 'pending';

COMMIT;
