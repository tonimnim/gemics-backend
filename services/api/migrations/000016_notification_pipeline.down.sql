BEGIN;

DROP TRIGGER IF EXISTS outbox_events_notification_enqueue ON outbox_events;
DROP FUNCTION IF EXISTS enqueue_notification_outbox_event();
DROP TABLE IF EXISTS notification_push_deliveries;
DROP TABLE IF EXISTS notification_outbox_queue;
DROP TABLE IF EXISTS notification_outbox_consumptions;

COMMIT;
