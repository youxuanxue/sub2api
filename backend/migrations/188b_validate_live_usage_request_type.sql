-- VALIDATE takes SHARE UPDATE EXCLUSIVE rather than holding the replacement
-- migration's ACCESS EXCLUSIVE lock for the duration of the table scan.
SET LOCAL lock_timeout = '5s';

ALTER TABLE usage_logs
    VALIDATE CONSTRAINT usage_logs_request_type_check;
