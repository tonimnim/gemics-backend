BEGIN;

-- The deadline worker reads only ready matches. Keeping the predicate aligned
-- with that state makes this index proportional to the live check-in queue,
-- rather than to the lifetime match table.
CREATE INDEX matches_ready_check_in_deadline_idx
    ON matches (check_in_closes_at, id)
    WHERE state = 'ready' AND check_in_closes_at IS NOT NULL;

COMMIT;
