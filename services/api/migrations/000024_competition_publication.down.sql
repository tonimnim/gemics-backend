BEGIN;

-- The previous API never reads published_at, so it can be dropped outright.
-- Rolled-back reads hide every cancelled competition again.
DROP TRIGGER IF EXISTS competitions_stamp_published_at ON competitions;
DROP FUNCTION IF EXISTS stamp_competition_published_at();
ALTER TABLE competitions DROP COLUMN IF EXISTS published_at;

COMMIT;
