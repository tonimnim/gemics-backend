BEGIN;

-- The pre-000012 schema cannot represent video evidence. Silently dropping the
-- media discriminator would leave video rows (and possibly result-evidence
-- links) behind rows that violate the restored image-only contract. Refuse the
-- rollback so operators must first archive/delete those objects and their
-- domain links deliberately.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM evidence_uploads WHERE media_kind = 'video') THEN
        RAISE EXCEPTION
            'cannot roll back 000012 while video evidence exists; archive or delete video evidence and its links first';
    END IF;
END
$$;

DROP INDEX IF EXISTS dispute_appeals_queue_idx;
DROP TABLE IF EXISTS dispute_appeals;
DROP INDEX IF EXISTS dispute_evidence_case_idx;
DROP TABLE IF EXISTS dispute_evidence;
DROP INDEX IF EXISTS dispute_evidence_requests_player_open_idx;
DROP INDEX IF EXISTS dispute_evidence_requests_case_idx;
DROP TABLE IF EXISTS dispute_evidence_requests;
DROP INDEX IF EXISTS dispute_events_case_timeline_idx;
DROP TABLE IF EXISTS dispute_events;

ALTER TABLE disputes
    DROP CONSTRAINT IF EXISTS disputes_appeal_window_chk,
    DROP CONSTRAINT IF EXISTS disputes_decision_payload_chk,
    DROP COLUMN IF EXISTS appeal_decided_at,
    DROP COLUMN IF EXISTS appeal_status,
    DROP COLUMN IF EXISTS appeal_deadline,
    DROP COLUMN IF EXISTS decided_by,
    DROP COLUMN IF EXISTS decision_note,
    DROP COLUMN IF EXISTS decision_code;

DROP INDEX IF EXISTS profile_media_uploads_pending_expiry_idx;
DROP INDEX IF EXISTS profile_media_uploads_user_created_idx;
DROP TABLE IF EXISTS profile_media_uploads;

DROP INDEX IF EXISTS evidence_media_processing_queue_idx;
DROP TABLE IF EXISTS evidence_media_processing_jobs;

UPDATE evidence_uploads
SET bound_kind = NULL, bound_id = NULL, bound_at = NULL
WHERE bound_kind IN ('dispute_case', 'game_account_verification');
UPDATE evidence_uploads
SET status = 'failed', processing_error_code = COALESCE(processing_error_code, 'migration_rollback')
WHERE status IN ('processing', 'rejected');

DROP INDEX IF EXISTS evidence_uploads_processing_idx;
ALTER TABLE evidence_uploads
    DROP CONSTRAINT IF EXISTS evidence_uploads_verified_video_ready_chk,
    DROP CONSTRAINT IF EXISTS evidence_uploads_processed_state_chk,
    DROP CONSTRAINT IF EXISTS evidence_uploads_processing_state_chk,
    DROP CONSTRAINT IF EXISTS evidence_uploads_media_duration_chk,
    DROP CONSTRAINT IF EXISTS evidence_uploads_bound_kind_v2_chk,
    DROP CONSTRAINT IF EXISTS evidence_uploads_status_v2_chk,
    DROP CONSTRAINT IF EXISTS evidence_uploads_byte_size_v2_chk,
    DROP CONSTRAINT IF EXISTS evidence_uploads_media_kind_chk,
    DROP CONSTRAINT IF EXISTS evidence_uploads_media_type_v2_chk,
    DROP COLUMN IF EXISTS processed_at,
    DROP COLUMN IF EXISTS processing_error_code,
    DROP COLUMN IF EXISTS verified_duration_seconds,
    DROP COLUMN IF EXISTS declared_duration_seconds,
    DROP COLUMN IF EXISTS media_kind,
    ADD CONSTRAINT evidence_uploads_media_type_check
        CHECK (media_type IN ('image/jpeg', 'image/png', 'image/heic', 'image/heif')) NOT VALID,
    ADD CONSTRAINT evidence_uploads_byte_size_check
        CHECK (byte_size > 0 AND byte_size <= 26214400) NOT VALID,
    ADD CONSTRAINT evidence_uploads_status_check
        CHECK (status IN ('pending', 'completed', 'failed', 'expired')),
    ADD CONSTRAINT evidence_uploads_bound_kind_check
        CHECK (bound_kind IN ('result_submission', 'result_dispute'));

COMMIT;
