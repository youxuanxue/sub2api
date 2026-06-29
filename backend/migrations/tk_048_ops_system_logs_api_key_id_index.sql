-- tk_048_ops_system_logs_api_key_id_index.sql
-- Upstream 155 creates idx_ops_system_logs_api_key_id_created_at on the plain
-- ops_system_logs table, but tk_035 converts the table to RANGE partitioning
-- earlier in TK rollout order (154/155 sort before tk_*). tk_035 renames legacy
-- indexes to *_legacy and recreates a fixed parent index set that predates
-- api_key_id — so fresh installs never get the canonical parent index. Recreate
-- it here with IF NOT EXISTS for idempotent prod rollout.
--
-- Uses a blocking CREATE INDEX (not *_notx.sql): PG rejects online index build on
-- partitioned parents. Runs at startup migration apply, matching tk_035.

CREATE INDEX IF NOT EXISTS idx_ops_system_logs_api_key_id_created_at
  ON ops_system_logs (api_key_id, created_at DESC);
