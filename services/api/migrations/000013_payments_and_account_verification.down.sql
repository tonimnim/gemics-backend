BEGIN;

UPDATE competition_entries entry SET status=CASE
    WHEN EXISTS (SELECT 1 FROM payment_refunds refund
        WHERE refund.entry_id=entry.id AND refund.status='succeeded') THEN 'withdrawn'
    ELSE 'registered'
END
WHERE status='withdrawal_pending';

-- Requests are soft domain records and evidence_uploads has no FK to bound_id.
-- Unbind before dropping the request/link tables so the preceding evidence
-- migration can later restore its narrower bound_kind constraint.
UPDATE evidence_uploads SET bound_kind=NULL,bound_id=NULL,bound_at=NULL,updated_at=now()
WHERE bound_kind='game_account_verification';

-- Accounts placed into pending by this workflow must not retain a state that
-- has no backing request after rollback.
UPDATE game_accounts account SET verification_status='unverified',verified_at=NULL,updated_at=now()
WHERE account.verification_status='pending' AND EXISTS (
    SELECT 1 FROM game_account_verification_requests request
    WHERE request.game_account_id=account.id AND request.status IN ('requested','under_review')
);

DROP TABLE IF EXISTS game_account_verification_evidence;
DROP TABLE IF EXISTS game_account_verification_requests;
ALTER TABLE game_accounts DROP CONSTRAINT IF EXISTS game_accounts_publisher_verified_chk;
ALTER TABLE game_accounts DROP COLUMN IF EXISTS publisher_verified;
ALTER TABLE game_accounts DROP COLUMN IF EXISTS verification_method;
DROP TABLE IF EXISTS platform_staff_roles;
DROP TABLE IF EXISTS payment_refunds;
DROP INDEX IF EXISTS payment_intents_id_user_entry_uidx;
ALTER TABLE payment_intents DROP CONSTRAINT IF EXISTS payment_intents_terminal_time_chk;
ALTER TABLE payment_intents ADD CONSTRAINT payment_intents_terminal_time_chk CHECK (
    status NOT IN ('succeeded','failed') OR completed_at IS NOT NULL
);
ALTER TABLE competition_entries DROP CONSTRAINT IF EXISTS competition_entries_status_check;
ALTER TABLE competition_entries ADD CONSTRAINT competition_entries_status_check
    CHECK (status IN ('registered','checked_in','accepted','withdrawn','disqualified'));

COMMIT;
