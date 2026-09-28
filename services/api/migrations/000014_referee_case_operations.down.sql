BEGIN;

DROP INDEX IF EXISTS disputes_referee_expiry_idx;
DROP INDEX IF EXISTS disputes_referee_assignee_v2_idx;
DROP INDEX IF EXISTS disputes_referee_queue_v2_idx;

ALTER TABLE disputes
    DROP CONSTRAINT IF EXISTS disputes_decision_payload_state_chk,
    DROP CONSTRAINT IF EXISTS disputes_decision_payload_object_chk,
    DROP CONSTRAINT IF EXISTS disputes_version_positive_chk,
    DROP COLUMN IF EXISTS decision_payload,
    DROP COLUMN IF EXISTS version;

COMMIT;
