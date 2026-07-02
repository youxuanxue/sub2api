-- tk_057_drop_ops_business_limited.sql
--
-- Remove TokenKey-only is_business_limited / business_limited_count metrics.
-- SLA now uses error_owner IN ('platform','provider') for fault numerators and
-- success + all final errors for denominators (see service/ops_sla_scope.go).
-- bluegreen-safe-destructive-ok: contract migration — old app ignores dropped columns;
-- new app no longer reads/writes is_business_limited or business_limited_count.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE ops_error_logs
  DROP COLUMN IF EXISTS is_business_limited;

ALTER TABLE ops_system_metrics
  DROP COLUMN IF EXISTS business_limited_count;

ALTER TABLE ops_metrics_hourly
  DROP COLUMN IF EXISTS business_limited_count;

ALTER TABLE ops_metrics_daily
  DROP COLUMN IF EXISTS business_limited_count;
