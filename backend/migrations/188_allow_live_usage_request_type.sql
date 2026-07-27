-- TokenKey replaces the upstream one-transaction constraint rewrite with the
-- online-safe 188a/188b sequence below. Keep this upstream filename as an audit
-- anchor so future merges cannot silently restore the long ACCESS EXCLUSIVE
-- lock path.
--
-- See:
--   188a_allow_live_usage_request_type_not_valid.sql
--   188b_validate_live_usage_request_type.sql
SELECT 1;
