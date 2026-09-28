BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM progression_events WHERE kind = 'standings_updated') THEN
        RAISE EXCEPTION USING
            MESSAGE = 'cannot roll back 000015_match_progression while standings_updated progression events exist',
            HINT = 'Archive or explicitly remove those audit events before retrying the rollback.';
    END IF;
END;
$$;

ALTER TABLE progression_events
    DROP CONSTRAINT progression_events_kind_check,
    ADD CONSTRAINT progression_events_kind_check CHECK (kind IN
        ('slot_filled', 'slot_voided', 'match_readied', 'match_forfeited', 'match_cancelled',
         'round_released', 'stage_completed', 'placements_written', 'subgraph_unwound'));

DROP TABLE IF EXISTS match_progression_applications;

COMMIT;
