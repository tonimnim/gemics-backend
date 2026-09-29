BEGIN;

-- Deploy the previous API before rolling back: this API reads
-- display_name_set_at.
--
-- Email-derived names the up migration replaced are not restored: the email
-- address must never be shown as a name.
ALTER TABLE users DROP COLUMN IF EXISTS display_name_set_at;

COMMIT;
