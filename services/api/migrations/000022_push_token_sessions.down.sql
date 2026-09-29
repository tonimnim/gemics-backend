BEGIN;

-- Deploy the previous API before rolling back: this API reads session_id.
--
-- Installations the up migration revoked stay revoked; the app registers its
-- token again on its next launch.
DROP INDEX IF EXISTS push_tokens_session_idx;
ALTER TABLE push_tokens DROP COLUMN IF EXISTS session_id;

COMMIT;
