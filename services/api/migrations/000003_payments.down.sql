BEGIN;
DROP TABLE IF EXISTS payment_callback_events;
DROP TABLE IF EXISTS payment_intents;
DROP INDEX IF EXISTS game_accounts_id_user_uidx;
COMMIT;
