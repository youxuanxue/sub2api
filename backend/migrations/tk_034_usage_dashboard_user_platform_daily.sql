-- TK-only per-(user, effective-platform, day) rollup table that backs the two
-- heaviest admin-page aggregations. Both previously scanned the full 30-day
-- created_at window of usage_logs (2.37M rows / 2.3 GB on prod): a wide-window
-- aggregation that touches ~half the table, which NO index can speed up (the
-- planner correctly prefers a bitmap heap scan, so a covering index is refused).
-- The only structural fix is to pre-aggregate the historical days so the page
-- reads tens-to-hundreds of rollup rows instead of ~1.25M raw rows:
--
--   * GetUserSpendingRanking (dashboard spending-ranking widget): EXPLAIN
--     ANALYZE 1,655 ms / 845K buffers.
--   * GetBatchUserUsageStats (admin Users page usage columns): EXPLAIN
--     ANALYZE 2,738 ms / 845K buffers + JIT.
--
-- Grain: one row per (user_id, platform, bucket_date). "platform" is the SAME
-- effective platform the live queries compute -- COALESCE(NULLIF(g.platform,''),
-- a.platform) -- so the rollup carries the per-platform breakdown
-- GetBatchUserUsageStats.ByPlatform needs (the existing usage_dashboard_daily
-- rollup is system-wide only and cannot serve it).
--
-- Metrics: actual_cost + the four token columns + request count, enough to
-- reconstruct both queries' outputs byte-for-byte for completed days. requests
-- and tokens are summed over EVERY row (including actual_cost = 0) because
-- GetUserSpendingRanking counts/sums them unconditionally; consumers that want
-- the billed-only total (GetBatchUserUsageStats) filter actual_cost in the
-- read path, not here.
--
-- Staleness boundary: this table is populated by DashboardAggregationService
-- (watermark-driven, ~1 min interval). The read path reads COMPLETED past days
-- from here and reads TODAY's partial day from raw usage_logs (a narrow window
-- the created_at index serves cheaply), so live "today" numbers are always
-- exact and history is at most one aggregation interval stale -- well inside the
-- UI's existing 30s cache tolerance.

CREATE TABLE IF NOT EXISTS usage_dashboard_user_platform_daily (
    bucket_date           DATE NOT NULL,
    user_id               BIGINT NOT NULL,
    platform              VARCHAR(50) NOT NULL,
    total_requests        BIGINT NOT NULL DEFAULT 0,
    input_tokens          BIGINT NOT NULL DEFAULT 0,
    output_tokens         BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens     BIGINT NOT NULL DEFAULT 0,
    actual_cost           DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_date, user_id, platform)
);

-- Read paths filter/aggregate by user_id over a date range (Users page batch
-- lookup) and scan a date range across all users (spending ranking). A
-- (user_id, bucket_date) secondary index serves the former; the PK's leading
-- bucket_date serves the latter.
CREATE INDEX IF NOT EXISTS idx_usage_dashboard_upd_user_date
    ON usage_dashboard_user_platform_daily (user_id, bucket_date);
