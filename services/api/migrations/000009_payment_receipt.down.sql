BEGIN;

ALTER TABLE payment_intents DROP CONSTRAINT IF EXISTS payment_intents_receipt_chk;

COMMIT;
