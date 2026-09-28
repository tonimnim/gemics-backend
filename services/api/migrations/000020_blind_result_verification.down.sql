BEGIN;

-- Step 5b of the up migration is not reversed: extended result deadlines and
-- retargeted inbox links stay as they are, and a match it cancelled keeps the
-- 'competition_cancelled' reason, which the guard below refuses to discard.
-- Archive those matches first; the up migration's audit rows list them.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM match_result_verifications)
       OR EXISTS (SELECT 1 FROM match_result_reviews)
       OR EXISTS (SELECT 1 FROM player_strikes)
       OR EXISTS (SELECT 1 FROM competition_entry_removals)
       OR EXISTS (SELECT 1 FROM evidence_uploads WHERE bound_kind = 'match_result_report')
       OR EXISTS (SELECT 1 FROM result_submissions WHERE origin <> 'legacy')
       OR EXISTS (SELECT 1 FROM matches WHERE completion_reason IN
                  ('report_timeout', 'response_timeout', 'no_result_reported', 'platform_review',
                   'competition_cancelled'))
       OR EXISTS (SELECT 1 FROM progression_events WHERE cause = 'platform_review')
       OR EXISTS (SELECT 1 FROM match_progression_applications WHERE cause = 'platform_review') THEN
        RAISE EXCEPTION USING
            MESSAGE = 'cannot roll back 000020_blind_result_verification while blind-report data exists',
            HINT = 'Archive and remove score reports, reviews, strikes, removals and their audit ledgers first.';
    END IF;
END;
$$;

DROP TABLE IF EXISTS player_strikes;
DROP TABLE IF EXISTS match_result_reviews;
DROP TABLE IF EXISTS match_result_report_evidence;
DROP TABLE IF EXISTS match_result_reports;
DROP TABLE IF EXISTS match_result_verifications;
DROP TABLE IF EXISTS competition_entry_removals;

ALTER TABLE evidence_uploads
    DROP CONSTRAINT IF EXISTS evidence_uploads_bound_kind_v3_chk,
    ADD CONSTRAINT evidence_uploads_bound_kind_v2_chk CHECK (bound_kind IN
        ('result_submission', 'result_dispute', 'dispute_case', 'game_account_verification'));
ALTER TABLE match_progression_applications
    DROP CONSTRAINT IF EXISTS match_progression_applications_cause_v2_chk,
    ADD CONSTRAINT match_progression_applications_cause_check CHECK (cause IN
        ('player_confirmation', 'referee', 'timeout_forfeit', 'withdrawal', 'disqualification', 'admin_correction'));
ALTER TABLE progression_events
    DROP CONSTRAINT IF EXISTS progression_events_cause_v2_chk,
    ADD CONSTRAINT progression_events_cause_check CHECK (cause IN
        ('draw', 'player_confirmation', 'referee', 'timeout_forfeit', 'withdrawal',
         'disqualification', 'admin_correction'));
ALTER TABLE matches
    DROP CONSTRAINT IF EXISTS matches_completion_reason_v2_chk,
    ADD CONSTRAINT matches_completion_reason_check CHECK (completion_reason IN
        ('played', 'walkover', 'double_no_show', 'referee', 'timeout_forfeit',
         'reset_not_required', 'correction_voided'));
ALTER TABLE result_submissions
    DROP CONSTRAINT IF EXISTS result_submissions_canonical_status_chk,
    DROP CONSTRAINT IF EXISTS result_submissions_origin_chk,
    DROP COLUMN IF EXISTS origin;

ALTER TABLE result_confirmation_evidence DROP CONSTRAINT IF EXISTS result_confirmation_evidence_retired_chk;
ALTER TABLE result_confirmations DROP CONSTRAINT IF EXISTS result_confirmations_retired_chk;
ALTER TABLE dispute_appeals DROP CONSTRAINT IF EXISTS dispute_appeals_retired_chk;
ALTER TABLE dispute_evidence DROP CONSTRAINT IF EXISTS dispute_evidence_retired_chk;
ALTER TABLE dispute_evidence_requests DROP CONSTRAINT IF EXISTS dispute_evidence_requests_retired_chk;
ALTER TABLE dispute_events DROP CONSTRAINT IF EXISTS dispute_events_retired_chk;
ALTER TABLE disputes DROP CONSTRAINT IF EXISTS disputes_retired_chk;

-- Former referees stay analysts. The up migration's audit rows record the
-- conversion, and restoring the role would re-grant a retired permission.
ALTER TABLE organization_members
    DROP CONSTRAINT IF EXISTS organization_members_role_v2_chk,
    ADD CONSTRAINT organization_members_role_check CHECK (role IN ('owner', 'admin', 'referee', 'analyst'));

COMMIT;
