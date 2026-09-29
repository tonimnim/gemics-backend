BEGIN;

-- A cancelled competition stays readable to players when it was ever public:
-- its entrants keep registrations and a cancellation push that link to it. A
-- draft cancelled before publication was never public and must stay hidden, so
-- the first publication is recorded durably instead of being inferred from the
-- audit trail, which is subject to retention.
ALTER TABLE competitions ADD COLUMN published_at timestamptz;

-- Every status after draft is reached through published, so the first write
-- of any public status is the publication, whichever path makes it.
CREATE FUNCTION stamp_competition_published_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.published_at IS NULL AND NEW.status NOT IN ('draft', 'cancelled') THEN
        NEW.published_at := now();
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER competitions_stamp_published_at
BEFORE INSERT OR UPDATE OF status ON competitions
FOR EACH ROW EXECUTE FUNCTION stamp_competition_published_at();

-- One-time backfill; ALTER TABLE holds the table lock until commit. A row in a
-- public status was published. A cancelled row was published when its
-- publication was audited, when its cancellation left a public status, or when
-- it holds an entry, which only an open registration accepts. The audited
-- publication time is used where it survives, otherwise the creation time.
UPDATE competitions competition SET published_at = COALESCE(
    (SELECT min(audit.occurred_at) FROM audit_events audit
     WHERE audit.subject_type = 'competition' AND audit.subject_id = competition.id::text
       AND audit.action = 'competition.published'),
    competition.created_at)
WHERE competition.status NOT IN ('draft', 'cancelled')
   OR (competition.status = 'cancelled' AND (
       EXISTS (SELECT 1 FROM audit_events audit
               WHERE audit.subject_type = 'competition' AND audit.subject_id = competition.id::text
                 AND (audit.action = 'competition.published'
                      OR (audit.action = 'competition.cancelled'
                          AND audit.before_state ->> 'status' <> 'draft')))
       OR EXISTS (SELECT 1 FROM competition_entries entry WHERE entry.competition_id = competition.id)));

COMMIT;
