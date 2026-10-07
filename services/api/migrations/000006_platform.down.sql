BEGIN;

DROP TRIGGER IF EXISTS outbox_events_notification_enqueue ON outbox_events;
DROP TABLE IF EXISTS notification_push_deliveries CASCADE;
DROP TABLE IF EXISTS notifications CASCADE;
DROP TABLE IF EXISTS notification_outbox_consumptions CASCADE;
DROP TABLE IF EXISTS notification_outbox_queue CASCADE;
DROP TABLE IF EXISTS idempotency_keys CASCADE;
DROP TABLE IF EXISTS outbox_events CASCADE;
DROP TABLE IF EXISTS audit_events CASCADE;
DROP FUNCTION IF EXISTS enqueue_notification_outbox_event();

COMMIT;
