BEGIN;

ALTER TABLE player_game_ratings
    ALTER COLUMN rating_version SET DEFAULT 0;
ALTER SEQUENCE player_game_rating_change_seq OWNED BY NONE;
DROP SEQUENCE player_game_rating_change_seq;

COMMIT;
