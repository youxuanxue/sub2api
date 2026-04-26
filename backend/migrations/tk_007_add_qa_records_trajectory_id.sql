-- Add trajectory_id to qa_records so redaction-first QA capture and Ops logs
-- can share a stable evidence correlation id.
ALTER TABLE qa_records
    ADD COLUMN IF NOT EXISTS trajectory_id text NULL;

COMMENT ON COLUMN qa_records.trajectory_id IS
    'Stable evidence correlation id shared across QA capture and Ops error logs.';

CREATE INDEX IF NOT EXISTS idx_qa_records_trajectory_created_at
    ON qa_records (trajectory_id, created_at)
    WHERE trajectory_id IS NOT NULL;
