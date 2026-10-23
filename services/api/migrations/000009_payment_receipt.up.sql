BEGIN;

-- A payment has an M-Pesa receipt exactly when it succeeded: the success
-- callback writes both at once, and no other path sets either.
ALTER TABLE payment_intents
    ADD CONSTRAINT payment_intents_receipt_chk CHECK (((status = 'succeeded'::text) = (provider_receipt IS NOT NULL)));

COMMIT;
