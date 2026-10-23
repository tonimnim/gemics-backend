BEGIN;

-- Push goes through Firebase Cloud Messaging (docs/push-notifications.md). FCM
-- accepts or refuses each push when it is sent, so the Expo receipt columns go,
-- and a delivery is delivered once FCM accepts it. Expo tokens cannot be sent
-- through FCM: they are deleted with their deliveries, the inbox keeps every
-- notification, and each installation registers again with its FCM token.

DELETE FROM push_tokens;

ALTER TABLE push_tokens RENAME COLUMN expo_push_token TO token;
ALTER TABLE push_tokens DROP CONSTRAINT push_tokens_expo_push_token_check;
ALTER TABLE push_tokens
    ADD CONSTRAINT push_tokens_token_check CHECK (((length(token) >= 32) AND (length(token) <= 4096)));

DROP INDEX notification_push_deliveries_receipt_due_idx;
DROP INDEX notification_push_deliveries_ticket_idx;
ALTER TABLE notification_push_deliveries
    DROP CONSTRAINT notification_push_deliveries_check1,
    DROP CONSTRAINT notification_push_deliveries_check2,
    DROP CONSTRAINT notification_push_deliveries_state_check,
    DROP CONSTRAINT notification_push_deliveries_receipt_attempts_check,
    DROP CONSTRAINT notification_push_deliveries_provider_ticket_id_check,
    DROP COLUMN receipt_attempts,
    DROP COLUMN receipt_next_attempt_at,
    DROP COLUMN receipt_lease_until;
ALTER TABLE notification_push_deliveries RENAME COLUMN provider_ticket_id TO provider_message_id;
ALTER TABLE notification_push_deliveries
    ADD CONSTRAINT notification_push_deliveries_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'submitting'::text, 'delivered'::text, 'retry'::text, 'permanent_failure'::text, 'suppressed'::text]))),
    ADD CONSTRAINT notification_push_deliveries_delivered_chk CHECK (((state <> 'delivered'::text) OR ((accepted_at IS NOT NULL) AND (provider_message_id IS NOT NULL) AND (sent_token_hash IS NOT NULL)))),
    ADD CONSTRAINT notification_push_deliveries_provider_message_id_check CHECK (((provider_message_id IS NULL) OR (length(provider_message_id) <= 256)));

COMMIT;
