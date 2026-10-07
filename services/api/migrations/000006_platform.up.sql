BEGIN;

-- Audit trail, transactional outbox, idempotency and notification delivery.

-- Functions

CREATE FUNCTION enqueue_notification_outbox_event() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO notification_outbox_queue
        (source_event_id,aggregate_id,event_type,payload,occurred_at,available_at)
    VALUES (NEW.id,NEW.aggregate_id,NEW.event_type,NEW.payload,NEW.occurred_at,NEW.available_at)
    ON CONFLICT (source_event_id) DO NOTHING;
    RETURN NEW;
END;
$$;

-- audit_events

CREATE TABLE audit_events (
    id bigint NOT NULL,
    organization_id uuid,
    actor_user_id uuid,
    action text NOT NULL,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    request_id text,
    before_state jsonb,
    after_state jsonb,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL
);
ALTER TABLE audit_events ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME audit_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);
ALTER TABLE ONLY audit_events
    ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
CREATE INDEX audit_events_retention_idx ON audit_events USING btree (occurred_at, id);
CREATE INDEX audit_org_idx ON audit_events USING btree (organization_id, occurred_at DESC);
CREATE INDEX audit_subject_idx ON audit_events USING btree (subject_type, subject_id, occurred_at DESC);

-- outbox_events

CREATE TABLE outbox_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    processed_at timestamp with time zone,
    last_error text
);
ALTER TABLE ONLY outbox_events
    ADD CONSTRAINT outbox_events_pkey PRIMARY KEY (id);
CREATE INDEX outbox_delivery_idx ON outbox_events USING btree (available_at, occurred_at) WHERE (processed_at IS NULL);
CREATE INDEX outbox_events_processed_idx ON outbox_events USING btree (processed_at, id) WHERE (processed_at IS NOT NULL);

-- idempotency_keys

CREATE TABLE idempotency_keys (
    scope text NOT NULL,
    key text NOT NULL,
    request_hash text NOT NULL,
    response_status integer,
    response_body jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL
);
ALTER TABLE ONLY idempotency_keys
    ADD CONSTRAINT idempotency_keys_pkey PRIMARY KEY (scope, key);
CREATE INDEX idempotency_expiry_idx ON idempotency_keys USING btree (expires_at);

-- notification_outbox_queue

CREATE TABLE notification_outbox_queue (
    notification_sequence bigint NOT NULL,
    source_event_id uuid NOT NULL,
    aggregate_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamp with time zone NOT NULL,
    available_at timestamp with time zone NOT NULL
);
ALTER TABLE notification_outbox_queue ALTER COLUMN notification_sequence ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME notification_outbox_queue_notification_sequence_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);
ALTER TABLE ONLY notification_outbox_queue
    ADD CONSTRAINT notification_outbox_queue_pkey PRIMARY KEY (notification_sequence);
ALTER TABLE ONLY notification_outbox_queue
    ADD CONSTRAINT notification_outbox_queue_source_event_id_key UNIQUE (source_event_id);
CREATE INDEX notification_outbox_queue_due_idx ON notification_outbox_queue USING btree (available_at, notification_sequence);

-- notification_outbox_consumptions

CREATE TABLE notification_outbox_consumptions (
    source_event_id uuid NOT NULL,
    event_type text NOT NULL,
    disposition text NOT NULL,
    recipient_count integer DEFAULT 0 NOT NULL,
    consumed_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT notification_outbox_consumptions_disposition_check CHECK ((disposition = ANY (ARRAY['projected'::text, 'ignored'::text, 'malformed'::text]))),
    CONSTRAINT notification_outbox_consumptions_recipient_count_check CHECK ((recipient_count >= 0))
);
ALTER TABLE ONLY notification_outbox_consumptions
    ADD CONSTRAINT notification_outbox_consumptions_pkey PRIMARY KEY (source_event_id);
CREATE INDEX notification_outbox_consumptions_consumed_idx ON notification_outbox_consumptions USING btree (consumed_at, source_event_id);

-- notifications

CREATE TABLE notifications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    source_event_id uuid,
    category text NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    data jsonb DEFAULT '{}'::jsonb NOT NULL,
    action_url text,
    read_at timestamp with time zone,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT notifications_action_url_check CHECK (((action_url IS NULL) OR (length(action_url) <= 1000))),
    CONSTRAINT notifications_body_check CHECK (((length(body) >= 1) AND (length(body) <= 2000))),
    CONSTRAINT notifications_category_check CHECK (((length(category) >= 1) AND (length(category) <= 64))),
    CONSTRAINT notifications_check CHECK (((expires_at IS NULL) OR (expires_at > created_at))),
    CONSTRAINT notifications_data_check CHECK ((jsonb_typeof(data) = 'object'::text)),
    CONSTRAINT notifications_title_check CHECK (((length(title) >= 1) AND (length(title) <= 160)))
);
ALTER TABLE ONLY notifications
    ADD CONSTRAINT notifications_pkey PRIMARY KEY (id);
CREATE INDEX notifications_expiry_idx ON notifications USING btree (expires_at) WHERE (expires_at IS NOT NULL);
CREATE INDEX notifications_inbox_idx ON notifications USING btree (user_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX notifications_source_event_uidx ON notifications USING btree (user_id, source_event_id) WHERE (source_event_id IS NOT NULL);
CREATE INDEX notifications_unread_idx ON notifications USING btree (user_id, created_at DESC, id DESC) WHERE (read_at IS NULL);

-- notification_push_deliveries

CREATE TABLE notification_push_deliveries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    notification_id uuid NOT NULL,
    push_token_id uuid NOT NULL,
    preference_key text NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_until timestamp with time zone,
    provider_ticket_id text,
    sent_token_hash bytea,
    last_error_code text,
    accepted_at timestamp with time zone,
    receipt_attempts integer DEFAULT 0 NOT NULL,
    receipt_next_attempt_at timestamp with time zone,
    receipt_lease_until timestamp with time zone,
    delivered_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT notification_push_deliveries_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT notification_push_deliveries_check CHECK (((state = 'submitting'::text) = (lease_until IS NOT NULL))),
    CONSTRAINT notification_push_deliveries_check1 CHECK (((state = 'checking_receipt'::text) = (receipt_lease_until IS NOT NULL))),
    CONSTRAINT notification_push_deliveries_check2 CHECK (((state <> ALL (ARRAY['accepted'::text, 'checking_receipt'::text, 'delivered'::text])) OR ((accepted_at IS NOT NULL) AND (provider_ticket_id IS NOT NULL) AND (sent_token_hash IS NOT NULL)))),
    CONSTRAINT notification_push_deliveries_check3 CHECK (((state = 'delivered'::text) = (delivered_at IS NOT NULL))),
    CONSTRAINT notification_push_deliveries_last_error_code_check CHECK (((last_error_code IS NULL) OR (length(last_error_code) <= 128))),
    CONSTRAINT notification_push_deliveries_preference_key_check CHECK ((preference_key = ANY (ARRAY['competition_push'::text, 'match_push'::text, 'result_push'::text]))),
    CONSTRAINT notification_push_deliveries_provider_ticket_id_check CHECK (((provider_ticket_id IS NULL) OR (length(provider_ticket_id) <= 256))),
    CONSTRAINT notification_push_deliveries_receipt_attempts_check CHECK ((receipt_attempts >= 0)),
    CONSTRAINT notification_push_deliveries_sent_token_hash_check CHECK (((sent_token_hash IS NULL) OR (octet_length(sent_token_hash) = 32))),
    CONSTRAINT notification_push_deliveries_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'submitting'::text, 'accepted'::text, 'checking_receipt'::text, 'delivered'::text, 'retry'::text, 'permanent_failure'::text, 'suppressed'::text])))
);
ALTER TABLE ONLY notification_push_deliveries
    ADD CONSTRAINT notification_push_deliveries_notification_id_push_token_id_key UNIQUE (notification_id, push_token_id);
ALTER TABLE ONLY notification_push_deliveries
    ADD CONSTRAINT notification_push_deliveries_pkey PRIMARY KEY (id);
CREATE INDEX notification_push_deliveries_due_idx ON notification_push_deliveries USING btree (next_attempt_at, id) WHERE (state = ANY (ARRAY['pending'::text, 'retry'::text, 'submitting'::text]));
CREATE INDEX notification_push_deliveries_receipt_due_idx ON notification_push_deliveries USING btree (receipt_next_attempt_at, id) WHERE (state = ANY (ARRAY['accepted'::text, 'checking_receipt'::text]));
CREATE INDEX notification_push_deliveries_ticket_idx ON notification_push_deliveries USING btree (provider_ticket_id) WHERE (provider_ticket_id IS NOT NULL);

-- Relationships

ALTER TABLE ONLY audit_events
    ADD CONSTRAINT audit_events_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES users(id);
ALTER TABLE ONLY audit_events
    ADD CONSTRAINT audit_events_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations(id);
ALTER TABLE ONLY notification_push_deliveries
    ADD CONSTRAINT notification_push_deliveries_notification_id_fkey FOREIGN KEY (notification_id) REFERENCES notifications(id) ON DELETE CASCADE;
ALTER TABLE ONLY notification_push_deliveries
    ADD CONSTRAINT notification_push_deliveries_push_token_id_fkey FOREIGN KEY (push_token_id) REFERENCES push_tokens(id) ON DELETE CASCADE;
ALTER TABLE ONLY notifications
    ADD CONSTRAINT notifications_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

-- Triggers

CREATE TRIGGER outbox_events_notification_enqueue AFTER INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION enqueue_notification_outbox_event();

COMMIT;
