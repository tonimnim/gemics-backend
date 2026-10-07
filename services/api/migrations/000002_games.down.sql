BEGIN;

DROP TABLE IF EXISTS leaderboard_snapshot_rows CASCADE;
DROP TABLE IF EXISTS leaderboard_snapshots CASCADE;
DROP TABLE IF EXISTS player_game_ratings CASCADE;
DROP TABLE IF EXISTS game_accounts CASCADE;
DROP TABLE IF EXISTS games CASCADE;
DROP SEQUENCE IF EXISTS player_game_rating_change_seq;
DROP FUNCTION IF EXISTS publish_leaderboard_snapshot(text, text, text);

COMMIT;
