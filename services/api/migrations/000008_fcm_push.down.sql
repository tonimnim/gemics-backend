BEGIN;

DELETE FROM push_tokens;

ALTER TABLE notification_push_deliveries
    DROP CONSTRAINT notification_push_deliveries_state_check,
    DROP CONSTRAINT notification_push_deliveries_delivered_chk,
    DROP CONSTRAINT notification_push_deliveries_provider_message_id_check;
ALTER TABLE notification_push_deliveries RENAME COLUMN provider_message_id TO provider_ticket_id;
ALTER TABLE notification_push_deliveries
    ADD COLUMN receipt_attempts integer DEFAULT 0 NOT NULL,
    ADD COLUMN receipt_next_attempt_at timestamp with time zone,
    ADD COLUMN receipt_lease_until timestamp with time zone,
    ADD CONSTRAINT notification_push_deliveries_receipt_attempts_check CHECK ((receipt_attempts >= 0)),
    ADD CONSTRAINT notification_push_deliveries_check1 CHECK (((state = 'checking_receipt'::text) = (receipt_lease_until IS NOT NULL))),
    ADD CONSTRAINT notification_push_deliveries_check2 CHECK (((state <> ALL (ARRAY['accepted'::text, 'checking_receipt'::text, 'delivered'::text])) OR ((accepted_at IS NOT NULL) AND (provider_ticket_id IS NOT NULL) AND (sent_token_hash IS NOT NULL)))),
    ADD CONSTRAINT notification_push_deliveries_provider_ticket_id_check CHECK (((provider_ticket_id IS NULL) OR (length(provider_ticket_id) <= 256))),
    ADD CONSTRAINT notification_push_deliveries_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'submitting'::text, 'accepted'::text, 'checking_receipt'::text, 'delivered'::text, 'retry'::text, 'permanent_failure'::text, 'suppressed'::text])));
CREATE INDEX notification_push_deliveries_receipt_due_idx ON notification_push_deliveries USING btree (receipt_next_attempt_at, id) WHERE (state = ANY (ARRAY['accepted'::text, 'checking_receipt'::text]));
CREATE INDEX notification_push_deliveries_ticket_idx ON notification_push_deliveries USING btree (provider_ticket_id) WHERE (provider_ticket_id IS NOT NULL);

ALTER TABLE push_tokens DROP CONSTRAINT push_tokens_token_check;
ALTER TABLE push_tokens RENAME COLUMN token TO expo_push_token;
ALTER TABLE push_tokens
    ADD CONSTRAINT push_tokens_expo_push_token_check CHECK (((length(expo_push_token) >= 20) AND (length(expo_push_token) <= 512)));

COMMIT;
