-- TK perf: drop unused usage_logs indexes.
--
-- usage_logs is the largest hot table (~2.65M rows / ~2.5GB, ~1.6GB of which is
-- indexes). Every API request writes a usage_log row, so each index is
-- write-amplification on the billing hot path. The indexes dropped below were
-- measured on prod (pg_stat_user_indexes) with idx_scan = 0 — i.e. the planner
-- has never used them since the last stats reset — while collectively occupying
-- ~450MB. Dropping them reduces INSERT cost, WAL volume, and disk pressure with
-- no read regression.
--
-- Notes:
--   * idx_usage_logs_created_model_upstream_model is functionally redundant with
--     idx_usage_logs_created_requested_model_upstream_model (migration 078),
--     which IS used heavily.
--   * We deliberately KEEP idx_usage_logs_model_created_at (low but non-zero use)
--     and idx_usage_logs_group_created_at_not_null (used by group queries).
--   * CONCURRENTLY + IF EXISTS → online, lock-free, idempotent (safe to replay).
--
-- ROLLOUT GUARD (operator): before applying, re-confirm idx_scan is still ~0 for
-- these on prod — idx_scan is cumulative since the last stats reset, so a fresh
-- check (ideally after enabling pg_stat_statements) avoids dropping an index that
-- a rare-but-important query depends on. Any dropped index can be recreated with
-- CREATE INDEX CONCURRENTLY if a regression appears.

DROP INDEX CONCURRENTLY IF EXISTS idx_usage_logs_created_model_upstream_model;
DROP INDEX CONCURRENTLY IF EXISTS idx_usage_logs_request_type_created_at;
DROP INDEX CONCURRENTLY IF EXISTS idx_usage_logs_sub_created;
DROP INDEX CONCURRENTLY IF EXISTS idx_usage_logs_service_tier_created_at;
DROP INDEX CONCURRENTLY IF EXISTS idx_usage_logs_subscription_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_usage_logs_ip_address;
DROP INDEX CONCURRENTLY IF EXISTS idx_usage_logs_model;
