BEGIN;

DROP TRIGGER IF EXISTS competitions_stamp_published_at ON competitions;
DROP TABLE IF EXISTS progression_events CASCADE;
DROP TABLE IF EXISTS match_progression_applications CASCADE;
DROP TABLE IF EXISTS match_check_ins CASCADE;
DROP TABLE IF EXISTS match_slots CASCADE;
DROP TABLE IF EXISTS competition_standings CASCADE;
DROP TABLE IF EXISTS competition_entry_removals CASCADE;
DROP TABLE IF EXISTS matches CASCADE;
DROP TABLE IF EXISTS competition_entry_placements CASCADE;
DROP TABLE IF EXISTS competition_draw_entries CASCADE;
DROP TABLE IF EXISTS competition_draws CASCADE;
DROP TABLE IF EXISTS entry_members CASCADE;
DROP TABLE IF EXISTS competition_entries CASCADE;
DROP TABLE IF EXISTS competition_stages CASCADE;
DROP TABLE IF EXISTS competitions CASCADE;
DROP TABLE IF EXISTS organization_members CASCADE;
DROP TABLE IF EXISTS organizations CASCADE;
DROP FUNCTION IF EXISTS stamp_competition_published_at();

COMMIT;
