-- Add trajectory_id to ops_error_logs so error events can be correlated with
-- QA capture and future unified evidence records.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE ops_error_logs
    ADD COLUMN IF NOT EXISTS trajectory_id VARCHAR(64);

COMMENT ON COLUMN ops_error_logs.trajectory_id IS
    'Stable evidence correlation id shared across QA capture and Ops error logs.';

CREATE INDEX IF NOT EXISTS idx_ops_error_logs_trajectory_created_at
    ON ops_error_logs (trajectory_id, created_at)
    WHERE trajectory_id IS NOT NULL;
