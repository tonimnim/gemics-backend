BEGIN;

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX competitions_public_name_trgm_idx
    ON competitions USING gin (lower(name) gin_trgm_ops)
    WHERE status IN ('published', 'registration_open', 'check_in', 'running', 'completed');

CREATE INDEX organizations_active_name_trgm_idx
    ON organizations USING gin (lower(name) gin_trgm_ops)
    WHERE status = 'active';

COMMIT;
