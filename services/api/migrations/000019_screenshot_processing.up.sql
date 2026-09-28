BEGIN;

ALTER TABLE evidence_uploads DROP CONSTRAINT evidence_uploads_processing_state_chk;
ALTER TABLE evidence_uploads ADD CONSTRAINT evidence_uploads_processing_state_chk CHECK (
    status <> 'processing' OR (completed_at IS NOT NULL AND processed_at IS NULL)
);

-- Queues are durable in PostgreSQL. Workers claim only their supported media,
-- and reclaim abandoned leases without processing the same job concurrently.
CREATE INDEX evidence_media_processing_running_lease_idx
    ON evidence_media_processing_jobs (locked_at, evidence_id) WHERE status='running';

COMMIT;
