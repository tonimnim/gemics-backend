BEGIN;

-- Evidence is screenshot-only. No worker ever claimed video jobs and no code
-- path could verify a video's duration, so in-flight video uploads and their
-- jobs are settled as failed instead of waiting forever.
UPDATE evidence_media_processing_jobs job SET status='failed',last_error='video_unsupported',
    locked_at=NULL,locked_by=NULL,finished_at=now(),updated_at=now()
FROM evidence_uploads evidence
WHERE evidence.id=job.evidence_id AND evidence.media_kind='video' AND job.status IN ('queued','running');
UPDATE evidence_uploads SET status='failed',processing_error_code='video_unsupported',updated_at=now()
WHERE media_kind='video' AND status IN ('pending','processing');

-- Error columns hold bounded identifiers only: the client-visible code and
-- the worker's internal class. Legacy free text (which could echo provider
-- responses) is cleared or replaced before the CHECKs are added.
UPDATE evidence_media_processing_jobs SET last_error=NULL
WHERE last_error IS NOT NULL AND last_error !~ '^[a-z][a-z0-9_]{0,63}$';
UPDATE evidence_uploads SET processing_error_code='legacy_error'
WHERE processing_error_code IS NOT NULL AND processing_error_code !~ '^[a-z][a-z0-9_]{0,63}$';

ALTER TABLE evidence_uploads
    ADD CONSTRAINT evidence_uploads_image_only_intake_chk
        CHECK (media_kind = 'image' OR status IN ('completed','failed','rejected','expired')),
    ADD CONSTRAINT evidence_uploads_processing_error_code_chk
        CHECK (processing_error_code IS NULL OR processing_error_code ~ '^[a-z][a-z0-9_]{0,63}$');

-- A claim that takes over an expired lease increments lease_recoveries. The
-- worker fails a job without touching storage once it exceeds the limit, so
-- an image that kills workers cannot be retried indefinitely. The worker's
-- claim reads this column: apply this migration before deploying it.
ALTER TABLE evidence_media_processing_jobs
    ADD COLUMN lease_recoveries integer NOT NULL DEFAULT 0 CHECK (lease_recoveries >= 0),
    ADD CONSTRAINT evidence_media_processing_jobs_last_error_class_chk
        CHECK (last_error IS NULL OR last_error ~ '^[a-z][a-z0-9_]{0,63}$');

COMMIT;
