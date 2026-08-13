BEGIN;

DROP INDEX IF EXISTS matches_away_history_schedule_idx;
DROP INDEX IF EXISTS matches_home_history_schedule_idx;
DROP INDEX IF EXISTS matches_away_active_schedule_idx;
DROP INDEX IF EXISTS matches_home_active_schedule_idx;
DROP INDEX IF EXISTS entry_members_user_entry_idx;
DROP TABLE IF EXISTS match_check_ins;
ALTER TABLE matches
    DROP CONSTRAINT IF EXISTS matches_check_in_window_pair_chk,
    DROP COLUMN IF EXISTS check_in_closes_at,
    DROP COLUMN IF EXISTS check_in_opens_at;

COMMIT;
