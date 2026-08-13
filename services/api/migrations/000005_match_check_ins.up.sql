BEGIN;

ALTER TABLE matches
    ADD COLUMN check_in_opens_at timestamptz,
    ADD COLUMN check_in_closes_at timestamptz;

-- Preserve sensible launch defaults for already-generated matches. New bracket
-- generation code should write the organizer's immutable schedule explicitly.
UPDATE matches
SET check_in_opens_at = scheduled_at - interval '15 minutes',
    check_in_closes_at = scheduled_at + interval '10 minutes'
WHERE scheduled_at IS NOT NULL;

ALTER TABLE matches
    ADD CONSTRAINT matches_check_in_window_pair_chk CHECK (
        (check_in_opens_at IS NULL AND check_in_closes_at IS NULL)
        OR (check_in_opens_at IS NOT NULL AND check_in_closes_at IS NOT NULL
            AND check_in_opens_at < check_in_closes_at)
    );

CREATE TABLE match_check_ins (
    match_id uuid NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    entry_id uuid NOT NULL REFERENCES competition_entries(id),
    checked_in_by uuid NOT NULL,
    match_version integer NOT NULL CHECK (match_version > 0),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
    checked_in_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (match_id, entry_id),
    CONSTRAINT match_check_ins_roster_member_fk
        FOREIGN KEY (entry_id, checked_in_by)
        REFERENCES entry_members(entry_id, user_id),
    UNIQUE (checked_in_by, idempotency_key)
);

CREATE INDEX entry_members_user_entry_idx
    ON entry_members (user_id, entry_id)
    WHERE roster_role IN ('starter', 'substitute');

CREATE INDEX matches_home_active_schedule_idx
    ON matches (home_entry_id, (COALESCE(completed_at, scheduled_at, created_at)), id)
    WHERE state IN ('pending', 'ready', 'in_progress', 'awaiting_confirmation', 'disputed');
CREATE INDEX matches_away_active_schedule_idx
    ON matches (away_entry_id, (COALESCE(completed_at, scheduled_at, created_at)), id)
    WHERE state IN ('pending', 'ready', 'in_progress', 'awaiting_confirmation', 'disputed');
CREATE INDEX matches_home_history_schedule_idx
    ON matches (home_entry_id, (COALESCE(completed_at, scheduled_at, created_at)), id)
    WHERE state IN ('completed', 'forfeit', 'cancelled');
CREATE INDEX matches_away_history_schedule_idx
    ON matches (away_entry_id, (COALESCE(completed_at, scheduled_at, created_at)), id)
    WHERE state IN ('completed', 'forfeit', 'cancelled');

COMMIT;
