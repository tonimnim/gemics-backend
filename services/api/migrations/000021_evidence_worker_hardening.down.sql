BEGIN;

-- Deploy the previous worker before rolling back: its claim does not read
-- lease_recoveries, but this worker's does.
--
-- Settled video uploads and jobs intentionally stay failed. No schema version
-- can verify a video, so returning them to pending or processing would only
-- strand them again. Replaced legacy error text is not restored either.
ALTER TABLE evidence_media_processing_jobs
    DROP CONSTRAINT IF EXISTS evidence_media_processing_jobs_last_error_class_chk,
    DROP COLUMN IF EXISTS lease_recoveries;

ALTER TABLE evidence_uploads
    DROP CONSTRAINT IF EXISTS evidence_uploads_processing_error_code_chk,
    DROP CONSTRAINT IF EXISTS evidence_uploads_image_only_intake_chk;

COMMIT;
