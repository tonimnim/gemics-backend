BEGIN;

-- rating_version was previously incremented per player. Taking max(version)
-- therefore failed to notice a lower-version player's result whenever another
-- player already held a larger value. A cached PostgreSQL sequence provides a
-- cheap global change watermark so the snapshot worker never misses a rating
-- mutation and does not need to scan/sort the leaderboard when nothing changed.
CREATE SEQUENCE player_game_rating_change_seq AS bigint CACHE 100;
SELECT setval(
    'player_game_rating_change_seq',
    GREATEST((SELECT COALESCE(max(rating_version), 0) FROM player_game_ratings), 1),
    (SELECT COALESCE(max(rating_version), 0) FROM player_game_ratings) > 0
);

UPDATE player_game_ratings
SET rating_version = nextval('player_game_rating_change_seq');

ALTER SEQUENCE player_game_rating_change_seq
    OWNED BY player_game_ratings.rating_version;
ALTER TABLE player_game_ratings
    ALTER COLUMN rating_version SET DEFAULT nextval('player_game_rating_change_seq');

COMMIT;
