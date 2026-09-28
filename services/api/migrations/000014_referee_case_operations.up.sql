BEGIN;

ALTER TABLE disputes
    ADD COLUMN version integer NOT NULL DEFAULT 1,
    ADD COLUMN decision_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD CONSTRAINT disputes_version_positive_chk CHECK (version > 0),
    ADD CONSTRAINT disputes_decision_payload_object_chk CHECK (jsonb_typeof(decision_payload) = 'object'),
    ADD CONSTRAINT disputes_decision_payload_state_chk CHECK (
        (decision_code IS NULL AND decision_payload = '{}'::jsonb)
        OR decision_code IS NOT NULL
    );

-- Organization isolation is supplied by the matches -> competitions join. These
-- indexes keep the bounded queue walk and detail lookup on dispute rows rather
-- than adding a denormalized organization id that could drift.
CREATE INDEX disputes_referee_queue_v2_idx
    ON disputes (status, opened_at DESC, id DESC)
    INCLUDE (assigned_to, match_id, version, appeal_status);
CREATE INDEX disputes_referee_assignee_v2_idx
    ON disputes (assigned_to, status, opened_at DESC, id DESC)
    INCLUDE (match_id, version, appeal_status)
    WHERE assigned_to IS NOT NULL;
CREATE INDEX disputes_referee_expiry_idx
    ON disputes (appeal_deadline, id)
    INCLUDE (match_id, version, decision_code)
    WHERE appeal_status = 'available';

COMMIT;
