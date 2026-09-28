BEGIN;

DROP TABLE IF EXISTS progression_events;
DROP TABLE IF EXISTS competition_standings;
DROP TABLE IF EXISTS competition_draw_entries;
DROP TABLE IF EXISTS competition_draws;
DROP TABLE IF EXISTS match_slots;

DROP INDEX IF EXISTS matches_release_idx;
DROP INDEX IF EXISTS matches_deadline_idx;
DROP INDEX IF EXISTS matches_stage_graph_idx;
DROP INDEX IF EXISTS matches_stage_open_idx;
DROP INDEX IF EXISTS matches_id_rank_uidx;
DROP INDEX IF EXISTS matches_id_competition_uidx;

ALTER TABLE matches
    DROP CONSTRAINT IF EXISTS matches_activation_pair_chk,
    DROP CONSTRAINT IF EXISTS matches_graph_rank_positive_chk,
    DROP COLUMN IF EXISTS completion_reason,
    DROP COLUMN IF EXISTS activation_source_match_id,
    DROP COLUMN IF EXISTS activation_rule,
    DROP COLUMN IF EXISTS group_key,
    DROP COLUMN IF EXISTS graph_rank;

ALTER TABLE matches RESET (fillfactor);

COMMIT;
