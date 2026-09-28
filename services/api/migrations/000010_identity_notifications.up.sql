BEGIN;

CREATE TABLE push_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id text NOT NULL CHECK (length(device_id) BETWEEN 1 AND 160),
    expo_push_token text NOT NULL CHECK (length(expo_push_token) BETWEEN 20 AND 512),
    platform text NOT NULL CHECK (platform IN ('android', 'ios')),
    app_version text NOT NULL DEFAULT '' CHECK (length(app_version) <= 64),
    user_agent text NOT NULL DEFAULT '',
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, device_id)
);
CREATE UNIQUE INDEX push_tokens_active_token_uidx
    ON push_tokens (expo_push_token) WHERE revoked_at IS NULL;
CREATE INDEX push_tokens_user_active_idx
    ON push_tokens (user_id, last_seen_at DESC) WHERE revoked_at IS NULL;

CREATE TABLE notifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Optional durable source identity for an at-least-once outbox projector.
    -- Do not FK this to outbox_events: delivered outbox rows may be archived.
    source_event_id uuid,
    category text NOT NULL CHECK (length(category) BETWEEN 1 AND 64),
    title text NOT NULL CHECK (length(title) BETWEEN 1 AND 160),
    body text NOT NULL CHECK (length(body) BETWEEN 1 AND 2000),
    data jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(data) = 'object'),
    action_url text CHECK (action_url IS NULL OR length(action_url) <= 1000),
    read_at timestamptz,
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at IS NULL OR expires_at > created_at)
);
CREATE INDEX notifications_inbox_idx
    ON notifications (user_id, created_at DESC, id DESC);
CREATE INDEX notifications_unread_idx
    ON notifications (user_id, created_at DESC, id DESC) WHERE read_at IS NULL;
CREATE INDEX notifications_expiry_idx
    ON notifications (expires_at) WHERE expires_at IS NOT NULL;
CREATE UNIQUE INDEX notifications_source_event_uidx
    ON notifications (user_id, source_event_id) WHERE source_event_id IS NOT NULL;

CREATE TABLE notification_preferences (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    competition_push boolean NOT NULL DEFAULT true,
    match_push boolean NOT NULL DEFAULT true,
    result_push boolean NOT NULL DEFAULT true,
    competition_email boolean NOT NULL DEFAULT true,
    match_email boolean NOT NULL DEFAULT true,
    marketing_email boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE legal_documents (
    document_type text NOT NULL CHECK (document_type IN ('terms', 'privacy')),
    version text NOT NULL CHECK (length(version) BETWEEN 1 AND 32),
    content_url text NOT NULL CHECK (length(content_url) BETWEEN 1 AND 1000),
    effective_at timestamptz NOT NULL,
    is_current boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (document_type, version)
);
CREATE UNIQUE INDEX legal_documents_one_current_uidx
    ON legal_documents (document_type) WHERE is_current = true;

INSERT INTO legal_documents (document_type, version, content_url, effective_at, is_current)
VALUES
    ('terms', '1.0', 'https://gamics.io/legal/terms', now(), true),
    ('privacy', '1.0', 'https://gamics.io/legal/privacy', now(), true);

CREATE TABLE legal_acceptances (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    document_type text NOT NULL,
    version text NOT NULL,
    accepted_at timestamptz NOT NULL DEFAULT now(),
    accepted_ip text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    PRIMARY KEY (user_id, document_type, version),
    FOREIGN KEY (document_type, version)
        REFERENCES legal_documents (document_type, version)
);
CREATE INDEX legal_acceptances_user_latest_idx
    ON legal_acceptances (user_id, document_type, accepted_at DESC);

CREATE TABLE account_deletion_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'cancelled', 'completed')),
    requested_at timestamptz NOT NULL DEFAULT now(),
    execute_after timestamptz NOT NULL,
    request_ip text NOT NULL DEFAULT '',
    cancelled_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (execute_after >= requested_at),
    CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL)),
    CHECK ((status = 'completed') = (completed_at IS NOT NULL))
);
CREATE UNIQUE INDEX account_deletion_requests_one_pending_uidx
    ON account_deletion_requests (user_id) WHERE status = 'pending';
CREATE INDEX account_deletion_requests_execution_idx
    ON account_deletion_requests (execute_after, id) WHERE status = 'pending';

COMMIT;
