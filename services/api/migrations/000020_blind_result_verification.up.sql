BEGIN;

-- 000020: blind dual score reports, Gamics result review, removal from the
-- tournament, and retirement of the organizer referee system.

-- 0. Take every table this migration alters before checking it. Each one gets
--    ACCESS EXCLUSIVE from its ALTER anyway; taking them up front closes the
--    check-then-act race with in-flight legacy writers and avoids lock-upgrade
--    deadlocks. The timeout makes a blocked rollout fail loudly instead of hanging.
SET LOCAL lock_timeout = '30s';
LOCK TABLE organization_members, matches, result_submissions, result_confirmations,
    result_confirmation_evidence, disputes, dispute_events, dispute_evidence_requests,
    dispute_evidence, dispute_appeals, progression_events, match_progression_applications,
    evidence_uploads
    IN ACCESS EXCLUSIVE MODE;

-- 1. Refuse to run while legacy confirm/dispute/referee work is in flight. The
--    new code has no reader for those states, so they could never finish.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM matches WHERE state IN ('awaiting_confirmation', 'disputed')) THEN
        RAISE EXCEPTION USING
            MESSAGE = 'cannot apply 000020_blind_result_verification while matches are awaiting confirmation or disputed',
            HINT = 'Finish every legacy confirmation and referee case with the previous release, then retry.';
    END IF;
    IF EXISTS (SELECT 1 FROM result_submissions WHERE status IN ('pending_confirmation', 'disputed')) THEN
        RAISE EXCEPTION USING
            MESSAGE = 'cannot apply 000020_blind_result_verification while legacy result submissions are pending or disputed',
            HINT = 'Finish every legacy confirmation and referee case with the previous release, then retry.';
    END IF;
    IF EXISTS (SELECT 1 FROM disputes
               WHERE status IN ('open', 'under_review') OR appeal_status IN ('available', 'pending'))
       OR EXISTS (SELECT 1 FROM dispute_appeals WHERE status = 'pending') THEN
        RAISE EXCEPTION USING
            MESSAGE = 'cannot apply 000020_blind_result_verification while referee cases or appeals are active',
            HINT = 'Decide or finalize every referee case and appeal with the previous release, then retry.';
    END IF;
END;
$$;

-- 2. Retire the organizer referee role. Analyst has the same read grants
--    (internal/organizer/roles.go). Every conversion is audited.
INSERT INTO audit_events
    (organization_id, actor_user_id, action, subject_type, subject_id, request_id, before_state, after_state)
SELECT organization_id, NULL, 'organization.member_role_changed', 'organization_member', user_id::text,
       'migration:000020', jsonb_build_object('role', 'referee'), jsonb_build_object('role', 'analyst')
FROM organization_members WHERE role = 'referee';
UPDATE organization_members SET role = 'analyst' WHERE role = 'referee';
ALTER TABLE organization_members
    DROP CONSTRAINT organization_members_role_check,
    ADD CONSTRAINT organization_members_role_v2_chk CHECK (role IN ('owner', 'admin', 'analyst'));

-- 3. Freeze the retired tables. Rows are kept for history and archived and
--    dropped by a later migration. CHECK (false) NOT VALID rejects every new
--    insert and every update of an existing row, while leaving history readable.
UPDATE dispute_evidence_requests SET status = 'cancelled' WHERE status = 'open';
ALTER TABLE disputes ADD CONSTRAINT disputes_retired_chk CHECK (false) NOT VALID;
ALTER TABLE dispute_events ADD CONSTRAINT dispute_events_retired_chk CHECK (false) NOT VALID;
ALTER TABLE dispute_evidence_requests ADD CONSTRAINT dispute_evidence_requests_retired_chk CHECK (false) NOT VALID;
ALTER TABLE dispute_evidence ADD CONSTRAINT dispute_evidence_retired_chk CHECK (false) NOT VALID;
ALTER TABLE dispute_appeals ADD CONSTRAINT dispute_appeals_retired_chk CHECK (false) NOT VALID;
ALTER TABLE result_confirmations ADD CONSTRAINT result_confirmations_retired_chk CHECK (false) NOT VALID;
ALTER TABLE result_confirmation_evidence
    ADD CONSTRAINT result_confirmation_evidence_retired_chk CHECK (false) NOT VALID;

-- 4. result_submissions stays the single canonical confirmed-result ledger read
--    by the bracket and public history. No pending/disputed rows again. The
--    preflight already proved there are none under the table lock, so the
--    CHECK is validated here and any slip fails loudly.
ALTER TABLE result_submissions
    ADD COLUMN origin text NOT NULL DEFAULT 'legacy',
    ADD CONSTRAINT result_submissions_origin_chk
        CHECK (origin IN ('legacy', 'agreed_reports', 'platform_review')),
    ADD CONSTRAINT result_submissions_canonical_status_chk
        CHECK (status NOT IN ('pending_confirmation', 'disputed'));

-- 5. Vocabulary. 'referee' stays allowed as a legacy history value; no code
--    writes it any more.
ALTER TABLE matches
    DROP CONSTRAINT matches_completion_reason_check,
    ADD CONSTRAINT matches_completion_reason_v2_chk CHECK (completion_reason IN
        ('played', 'walkover', 'double_no_show', 'referee', 'timeout_forfeit',
         'reset_not_required', 'correction_voided',
         'report_timeout', 'response_timeout', 'no_result_reported', 'platform_review',
         'competition_cancelled'));
ALTER TABLE progression_events
    DROP CONSTRAINT progression_events_cause_check,
    ADD CONSTRAINT progression_events_cause_v2_chk CHECK (cause IN
        ('draw', 'player_confirmation', 'referee', 'timeout_forfeit', 'withdrawal',
         'disqualification', 'admin_correction', 'platform_review'));
ALTER TABLE match_progression_applications
    DROP CONSTRAINT match_progression_applications_cause_check,
    ADD CONSTRAINT match_progression_applications_cause_v2_chk CHECK (cause IN
        ('player_confirmation', 'referee', 'timeout_forfeit', 'withdrawal',
         'disqualification', 'admin_correction', 'platform_review'));
ALTER TABLE evidence_uploads
    DROP CONSTRAINT evidence_uploads_bound_kind_v2_chk,
    ADD CONSTRAINT evidence_uploads_bound_kind_v3_chk CHECK (bound_kind IN
        ('result_submission', 'result_dispute', 'dispute_case',
         'game_account_verification', 'match_result_report'));

-- 5b. Legacy data, so the new rules apply only from this rollout onward. The
--     match changes are audited under request_id 'migration:000020'. None of
--     this step is reversed by the down migration.
--     A cancelled competition keeps no live match (T17 does this going forward).
WITH target AS (
    SELECT m.id, m.state, c.organization_id
    FROM matches m JOIN competitions c ON c.id = m.competition_id
    WHERE c.status = 'cancelled' AND m.state IN ('pending', 'ready', 'in_progress')
), cancelled AS (
    UPDATE matches m SET state = 'cancelled', winner_entry_id = NULL,
        completion_reason = 'competition_cancelled', completed_at = now(),
        version = m.version + 1, updated_at = now()
    FROM target WHERE m.id = target.id
    RETURNING m.id
)
INSERT INTO audit_events
    (organization_id, actor_user_id, action, subject_type, subject_id, request_id, before_state, after_state)
SELECT target.organization_id, NULL, 'match.cancelled', 'match', target.id::text, 'migration:000020',
       jsonb_build_object('state', target.state),
       jsonb_build_object('state', 'cancelled', 'completionReason', 'competition_cancelled')
FROM target JOIN cancelled ON cancelled.id = target.id;

--     In-progress matches whose result window already elapsed could never be
--     resolved under the old rules. Give them a fresh window instead of
--     applying R7 (removal of both entries) retroactively.
WITH target AS (
    SELECT m.id, m.result_due_at, c.organization_id
    FROM matches m JOIN competitions c ON c.id = m.competition_id
    WHERE m.state = 'in_progress' AND m.result_due_at IS NOT NULL AND m.result_due_at <= now()
), extended AS (
    UPDATE matches m SET result_due_at = now() + interval '24 hours',
        version = m.version + 1, updated_at = now()
    FROM target WHERE m.id = target.id
    RETURNING m.id, m.result_due_at
)
INSERT INTO audit_events
    (organization_id, actor_user_id, action, subject_type, subject_id, request_id, before_state, after_state)
SELECT target.organization_id, NULL, 'match.result_deadline_extended', 'match', target.id::text,
       'migration:000020', jsonb_build_object('resultDueAt', target.result_due_at),
       jsonb_build_object('resultDueAt', extended.result_due_at, 'reason', 'blind_result_verification_rollout')
FROM target JOIN extended ON extended.id = target.id;

--     Inbox rows already projected for referee cases point at retired routes.
UPDATE notifications
SET action_url = '/matches/' || (data ->> 'matchId'), data = data - 'caseId'
WHERE action_url LIKE '/referee-cases/%' AND data ? 'matchId';

-- 6. One verification row per match that received a first report. Deadlines
--    are snapshotted, so later rule edits never move a live deadline.
CREATE TABLE match_result_verifications (
    match_id uuid PRIMARY KEY,
    competition_id uuid NOT NULL,
    phase text NOT NULL CHECK (phase IN
        ('awaiting_second_report', 'awaiting_responses', 'in_review', 'resolved')),
    first_report_entry_id uuid NOT NULL,
    first_reported_at timestamptz NOT NULL,
    report_window_seconds integer NOT NULL CHECK (report_window_seconds BETWEEN 300 AND 3600),
    reminder_lead_seconds integer NOT NULL CHECK (reminder_lead_seconds >= 60),
    response_window_seconds integer NOT NULL CHECK (response_window_seconds BETWEEN 300 AND 3600),
    report_deadline_at timestamptz NOT NULL,
    reminder_at timestamptz NOT NULL,
    reminder_sent_at timestamptz,
    mismatch_at timestamptz,
    response_deadline_at timestamptz,
    resolution text CHECK (resolution IN
        ('agreed', 'report_timeout', 'response_timeout', 'platform_review', 'competition_cancelled')),
    resolved_at timestamptz,
    canonical_submission_id uuid,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (match_id, competition_id) REFERENCES matches (id, competition_id) ON DELETE CASCADE,
    FOREIGN KEY (first_report_entry_id, competition_id) REFERENCES competition_entries (id, competition_id),
    FOREIGN KEY (canonical_submission_id, match_id) REFERENCES result_submissions (id, match_id),
    CONSTRAINT match_result_verifications_reminder_lead_chk
        CHECK (reminder_lead_seconds <= report_window_seconds - 60),
    CONSTRAINT match_result_verifications_report_window_chk
        CHECK (first_reported_at < reminder_at AND reminder_at < report_deadline_at),
    CONSTRAINT match_result_verifications_mismatch_pair_chk
        CHECK ((mismatch_at IS NULL) = (response_deadline_at IS NULL)
               AND (response_deadline_at IS NULL OR response_deadline_at > mismatch_at)),
    CONSTRAINT match_result_verifications_phase_chk CHECK (
        (phase = 'awaiting_second_report' AND mismatch_at IS NULL AND resolution IS NULL)
        OR (phase IN ('awaiting_responses', 'in_review') AND mismatch_at IS NOT NULL AND resolution IS NULL)
        OR (phase = 'resolved' AND resolution IS NOT NULL)),
    CONSTRAINT match_result_verifications_resolved_at_chk
        CHECK ((resolution IS NULL) = (resolved_at IS NULL))
);
-- Worker queues: each index is proportional to the live queue (000018 pattern).
CREATE INDEX match_result_verifications_reminder_idx
    ON match_result_verifications (reminder_at, match_id)
    WHERE phase = 'awaiting_second_report' AND reminder_sent_at IS NULL;
CREATE INDEX match_result_verifications_report_deadline_idx
    ON match_result_verifications (report_deadline_at, match_id) WHERE phase = 'awaiting_second_report';
CREATE INDEX match_result_verifications_response_deadline_idx
    ON match_result_verifications (response_deadline_at, match_id) WHERE phase = 'awaiting_responses';

-- 7. Blind reports: exactly one initial and at most one final per entry. The
--    tiebreak scores are named NOT NULL explicitly because a NULL comparison
--    would otherwise satisfy the CHECK (000006 pattern).
CREATE TABLE match_result_reports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    match_id uuid NOT NULL REFERENCES match_result_verifications (match_id) ON DELETE CASCADE,
    competition_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    reported_by uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('initial', 'final')),
    home_score integer NOT NULL CHECK (home_score BETWEEN 0 AND 99),
    away_score integer NOT NULL CHECK (away_score BETWEEN 0 AND 99),
    tiebreak_type text,
    home_tiebreak_score integer,
    away_tiebreak_score integer,
    game_results jsonb NOT NULL,
    match_version integer NOT NULL CHECK (match_version > 0),
    reported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (match_id, entry_id, kind),
    FOREIGN KEY (match_id, competition_id) REFERENCES matches (id, competition_id) ON DELETE CASCADE,
    FOREIGN KEY (entry_id, competition_id) REFERENCES competition_entries (id, competition_id),
    FOREIGN KEY (entry_id, reported_by) REFERENCES entry_members (entry_id, user_id),
    CONSTRAINT match_result_reports_tiebreak_chk CHECK (
        (tiebreak_type IS NULL AND home_tiebreak_score IS NULL AND away_tiebreak_score IS NULL)
        OR (tiebreak_type = 'penalties' AND home_score = away_score
            AND home_tiebreak_score IS NOT NULL AND away_tiebreak_score IS NOT NULL
            AND home_tiebreak_score BETWEEN 0 AND 99 AND away_tiebreak_score BETWEEN 0 AND 99
            AND home_tiebreak_score <> away_tiebreak_score)),
    CONSTRAINT match_result_reports_games_array_chk CHECK (
        jsonb_typeof(game_results) = 'array' AND jsonb_array_length(game_results) BETWEEN 1 AND 99)
);

CREATE TABLE match_result_report_evidence (
    report_id uuid NOT NULL REFERENCES match_result_reports (id) ON DELETE CASCADE,
    evidence_id uuid NOT NULL UNIQUE REFERENCES evidence_uploads (id),
    position smallint NOT NULL CHECK (position BETWEEN 0 AND 2),
    PRIMARY KEY (report_id, evidence_id),
    UNIQUE (report_id, position)
);

-- 8. Gamics review queue. decider_kind='system' is reserved for a future AI
--    decider; it can never author a corrected score (submitted_by is a user).
--    'closed' means the competition was cancelled before a decision, so a
--    closed review carries no decision and no decider.
CREATE TABLE match_result_reviews (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    match_id uuid NOT NULL UNIQUE REFERENCES match_result_verifications (match_id) ON DELETE CASCADE,
    competition_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'decided', 'closed')),
    reason text NOT NULL CHECK (reason IN ('reports_differ', 'evidence_unavailable')),
    queued_at timestamptz NOT NULL DEFAULT now(),
    decision text CHECK (decision IN ('accept_home', 'accept_away', 'corrected_score', 'remove_both')),
    corrected_home_score integer CHECK (corrected_home_score BETWEEN 0 AND 99),
    corrected_away_score integer CHECK (corrected_away_score BETWEEN 0 AND 99),
    corrected_tiebreak_type text,
    corrected_home_tiebreak_score integer,
    corrected_away_tiebreak_score integer,
    corrected_game_results jsonb,
    note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 2000),
    decider_kind text CHECK (decider_kind IN ('staff', 'system')),
    decided_by uuid REFERENCES users (id),
    decider_ref text CHECK (decider_ref IS NULL OR char_length(decider_ref) BETWEEN 1 AND 128),
    decided_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (match_id, competition_id) REFERENCES matches (id, competition_id) ON DELETE CASCADE,
    CONSTRAINT match_result_reviews_decision_state_chk CHECK (
        (status = 'queued' AND decision IS NULL AND decider_kind IS NULL AND decided_by IS NULL
            AND decider_ref IS NULL AND decided_at IS NULL)
        OR (status = 'decided' AND decision IS NOT NULL AND decider_kind IS NOT NULL AND decided_at IS NOT NULL)
        OR (status = 'closed' AND decision IS NULL AND decider_kind IS NULL AND decided_by IS NULL
            AND decider_ref IS NULL AND decided_at IS NOT NULL)),
    CONSTRAINT match_result_reviews_decider_chk CHECK (
        decider_kind IS NULL
        OR (decider_kind = 'staff' AND decided_by IS NOT NULL)
        OR (decider_kind = 'system' AND decided_by IS NULL AND decider_ref IS NOT NULL
            AND decision <> 'corrected_score')),
    CONSTRAINT match_result_reviews_corrected_chk CHECK (
        (decision IS DISTINCT FROM 'corrected_score'
            AND corrected_home_score IS NULL AND corrected_away_score IS NULL
            AND corrected_game_results IS NULL AND corrected_tiebreak_type IS NULL
            AND corrected_home_tiebreak_score IS NULL AND corrected_away_tiebreak_score IS NULL)
        OR (decision = 'corrected_score' AND corrected_home_score IS NOT NULL
            AND corrected_away_score IS NOT NULL AND corrected_game_results IS NOT NULL
            AND jsonb_typeof(corrected_game_results) = 'array'
            AND jsonb_array_length(corrected_game_results) BETWEEN 1 AND 99)),
    CONSTRAINT match_result_reviews_corrected_tiebreak_chk CHECK (
        (corrected_tiebreak_type IS NULL AND corrected_home_tiebreak_score IS NULL
            AND corrected_away_tiebreak_score IS NULL)
        OR (corrected_tiebreak_type = 'penalties' AND corrected_home_score = corrected_away_score
            AND corrected_home_tiebreak_score IS NOT NULL AND corrected_away_tiebreak_score IS NOT NULL
            AND corrected_home_tiebreak_score BETWEEN 0 AND 99 AND corrected_away_tiebreak_score BETWEEN 0 AND 99
            AND corrected_home_tiebreak_score <> corrected_away_tiebreak_score))
);
CREATE INDEX match_result_reviews_queue_idx ON match_result_reviews (queued_at, id) WHERE status = 'queued';
CREATE INDEX match_result_reviews_decided_idx
    ON match_result_reviews (decided_at DESC, id DESC) WHERE status = 'decided';
CREATE INDEX match_result_reviews_competition_idx ON match_result_reviews (competition_id, queued_at, id);

-- 9. Strikes: active until revoked; counted by eligibility. Only platform staff
--    record strikes; a future automated decider must not trigger a ban.
CREATE TABLE player_strikes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id),
    match_id uuid NOT NULL REFERENCES matches (id),
    review_id uuid NOT NULL REFERENCES match_result_reviews (id),
    reason_code text NOT NULL CHECK (reason_code = 'false_result_report'),
    note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 2000),
    created_by_kind text NOT NULL CHECK (created_by_kind = 'staff'),
    created_by uuid REFERENCES users (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    revoked_by uuid REFERENCES users (id),
    revoke_reason text,
    UNIQUE (review_id, user_id),
    CONSTRAINT player_strikes_creator_chk CHECK ((created_by_kind = 'staff') = (created_by IS NOT NULL)),
    CONSTRAINT player_strikes_revocation_chk CHECK (
        (revoked_at IS NULL AND revoked_by IS NULL AND revoke_reason IS NULL)
        OR (revoked_at IS NOT NULL AND revoked_by IS NOT NULL AND revoke_reason IS NOT NULL
            AND char_length(revoke_reason) BETWEEN 10 AND 500))
);
CREATE INDEX player_strikes_active_user_idx ON player_strikes (user_id) WHERE revoked_at IS NULL;
CREATE INDEX player_strikes_user_history_idx ON player_strikes (user_id, created_at DESC, id DESC);
CREATE INDEX player_strikes_feed_idx ON player_strikes (created_at DESC, id DESC);

-- 10. Why an entry left the tournament. The primary key makes removal idempotent.
CREATE TABLE competition_entry_removals (
    entry_id uuid PRIMARY KEY,
    competition_id uuid NOT NULL,
    match_id uuid NOT NULL,
    reason_code text NOT NULL CHECK (reason_code IN
        ('report_timeout', 'response_timeout', 'no_result_reported', 'platform_review')),
    previous_status text NOT NULL CHECK (previous_status IN
        ('registered', 'checked_in', 'accepted', 'withdrawal_pending')),
    actor_kind text NOT NULL CHECK (actor_kind IN ('worker', 'staff', 'system')),
    actor_user_id uuid REFERENCES users (id),
    removed_at timestamptz NOT NULL,
    FOREIGN KEY (entry_id, competition_id) REFERENCES competition_entries (id, competition_id),
    FOREIGN KEY (match_id, competition_id) REFERENCES matches (id, competition_id),
    CONSTRAINT competition_entry_removals_actor_chk CHECK ((actor_kind = 'staff') = (actor_user_id IS NOT NULL))
);
CREATE INDEX competition_entry_removals_competition_idx
    ON competition_entry_removals (competition_id, removed_at, entry_id);

COMMIT;
