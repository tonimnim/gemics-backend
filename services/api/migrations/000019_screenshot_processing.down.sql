BEGIN;

-- Rollback must drain image processing first; fail rather than lose work.
ALTER TABLE evidence_uploads DROP CONSTRAINT evidence_uploads_processing_state_chk;
ALTER TABLE evidence_uploads ADD CONSTRAINT evidence_uploads_processing_state_chk CHECK (
    status <> 'processing' OR (media_kind='video' AND completed_at IS NOT NULL AND processed_at IS NULL)
);
DROP INDEX evidence_media_processing_running_lease_idx;

COMMIT;
