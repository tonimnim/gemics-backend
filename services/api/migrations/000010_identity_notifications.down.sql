BEGIN;

DROP TABLE IF EXISTS account_deletion_requests;
DROP TABLE IF EXISTS legal_acceptances;
DROP TABLE IF EXISTS legal_documents;
DROP TABLE IF EXISTS notification_preferences;
DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS push_tokens;

COMMIT;
