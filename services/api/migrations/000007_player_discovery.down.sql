BEGIN;

DROP INDEX IF EXISTS matches_public_history_idx;
DROP INDEX IF EXISTS entry_members_user_history_idx;
DROP INDEX IF EXISTS game_accounts_in_game_name_trgm_idx;
DROP INDEX IF EXISTS users_active_display_name_trgm_idx;
DROP INDEX IF EXISTS player_profiles_discoverable_handle_trgm_idx;
DROP FUNCTION IF EXISTS publish_leaderboard_snapshot(text, text, text);
DROP TABLE IF EXISTS competition_entry_placements;
DROP TABLE IF EXISTS leaderboard_snapshot_rows;
DROP TABLE IF EXISTS leaderboard_snapshots;
DROP TABLE IF EXISTS player_game_ratings;

-- pg_trgm may be shared by another feature, so this migration intentionally does
-- not remove the extension during a rollback.

COMMIT;
