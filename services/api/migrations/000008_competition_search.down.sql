BEGIN;

DROP INDEX IF EXISTS organizations_active_name_trgm_idx;
DROP INDEX IF EXISTS competitions_public_name_trgm_idx;

COMMIT;
