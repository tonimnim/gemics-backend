BEGIN;

-- Outbox rows are shared by independent consumers.  This ledger records only
-- the notification projector's disposition and deliberately leaves
-- outbox_events.processed_at untouched.
CREATE TABLE notification_outbox_consumptions (
    source_event_id uuid PRIMARY KEY,
    event_type text NOT NULL,
    disposition text NOT NULL CHECK (disposition IN ('projected','ignored','malformed')),
    recipient_count integer NOT NULL DEFAULT 0 CHECK (recipient_count >= 0),
    consumed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notification_outbox_consumptions_consumed_idx
    ON notification_outbox_consumptions (consumed_at, source_event_id);

-- A consumer-specific transactional queue avoids repeatedly anti-joining the
-- complete outbox history. The trigger copies immutable event data in the same
-- transaction as the domain write, so rolled-back/out-of-order identity values
-- cannot create cursor gaps and delayed available_at rows cannot be skipped.
CREATE TABLE notification_outbox_queue (
    notification_sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_event_id uuid NOT NULL UNIQUE,
    aggregate_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL
);
CREATE INDEX notification_outbox_queue_due_idx
    ON notification_outbox_queue (available_at, notification_sequence);

CREATE FUNCTION enqueue_notification_outbox_event() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO notification_outbox_queue
        (source_event_id,aggregate_id,event_type,payload,occurred_at,available_at)
    VALUES (NEW.id,NEW.aggregate_id,NEW.event_type,NEW.payload,NEW.occurred_at,NEW.available_at)
    ON CONFLICT (source_event_id) DO NOTHING;
    RETURN NEW;
END;
$$;
CREATE TRIGGER outbox_events_notification_enqueue
AFTER INSERT ON outbox_events
FOR EACH ROW EXECUTE FUNCTION enqueue_notification_outbox_event();

-- One-time migration backfill. CREATE TRIGGER's table lock remains held until
-- commit, so writers cannot fall between this snapshot and trigger activation.
INSERT INTO notification_outbox_queue
    (source_event_id,aggregate_id,event_type,payload,occurred_at,available_at)
SELECT id,aggregate_id,event_type,payload,occurred_at,available_at
FROM outbox_events
ORDER BY occurred_at,id
ON CONFLICT (source_event_id) DO NOTHING;

-- Delivery is one row per inbox notification and installation.  A bounded
-- lease lets another replica recover work after a process dies while keeping
-- provider calls outside database transactions.
CREATE TABLE notification_push_deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    notification_id uuid NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
    push_token_id uuid NOT NULL REFERENCES push_tokens(id) ON DELETE CASCADE,
    preference_key text NOT NULL CHECK (preference_key IN (
        'competition_push','match_push','result_push'
    )),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN (
        'pending','submitting','accepted','checking_receipt','delivered',
        'retry','permanent_failure','suppressed'
    )),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    provider_ticket_id text CHECK (
        provider_ticket_id IS NULL OR length(provider_ticket_id) <= 256
    ),
    sent_token_hash bytea CHECK (
        sent_token_hash IS NULL OR octet_length(sent_token_hash) = 32
    ),
    last_error_code text CHECK (
        last_error_code IS NULL OR length(last_error_code) <= 128
    ),
    accepted_at timestamptz,
    receipt_attempts integer NOT NULL DEFAULT 0 CHECK (receipt_attempts >= 0),
    receipt_next_attempt_at timestamptz,
    receipt_lease_until timestamptz,
    delivered_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (notification_id, push_token_id),
    CHECK ((state = 'submitting') = (lease_until IS NOT NULL)),
    CHECK ((state = 'checking_receipt') = (receipt_lease_until IS NOT NULL)),
    CHECK (state NOT IN ('accepted','checking_receipt','delivered') OR
        (accepted_at IS NOT NULL AND provider_ticket_id IS NOT NULL AND sent_token_hash IS NOT NULL)),
    CHECK ((state = 'delivered') = (delivered_at IS NOT NULL))
);
CREATE INDEX notification_push_deliveries_due_idx
    ON notification_push_deliveries (next_attempt_at, id)
    WHERE state IN ('pending','retry','submitting');
CREATE INDEX notification_push_deliveries_ticket_idx
    ON notification_push_deliveries (provider_ticket_id)
    WHERE provider_ticket_id IS NOT NULL;
CREATE INDEX notification_push_deliveries_receipt_due_idx
    ON notification_push_deliveries (receipt_next_attempt_at, id)
    WHERE state IN ('accepted','checking_receipt');

COMMIT;
