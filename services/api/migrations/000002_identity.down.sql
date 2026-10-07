BEGIN;
DROP TABLE IF EXISTS login_failures;
DROP TABLE IF EXISTS refresh_sessions;
DROP TABLE IF EXISTS email_otp_challenges;
ALTER TABLE users
    DROP COLUMN IF EXISTS privacy_accepted_at,
    DROP COLUMN IF EXISTS terms_accepted_at;
COMMIT;
