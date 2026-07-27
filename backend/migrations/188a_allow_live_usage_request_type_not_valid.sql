-- Keep the ACCESS EXCLUSIVE lock window bounded: replacing the constraint is
-- metadata-only, while the full-table scan runs in the follow-up migration.
SET LOCAL lock_timeout = '5s';

ALTER TABLE usage_logs
    DROP CONSTRAINT IF EXISTS usage_logs_request_type_check;

ALTER TABLE usage_logs
    ADD CONSTRAINT usage_logs_request_type_check
    CHECK (request_type >= 0 AND request_type <= 5) NOT VALID;
