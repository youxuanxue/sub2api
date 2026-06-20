-- TK-only per-(group, day) cost rollup that backs the admin Groups page
-- usage-summary widget (GET /api/v1/admin/groups/usage-summary).
--
-- The legacy GetAllGroupUsageSummary does
--     SELECT g.id, SUM(ul.actual_cost) AS total_cost, SUM(... today ...) AS today_cost
--     FROM groups g LEFT JOIN usage_logs ul ON ul.group_id = g.id GROUP BY g.id
-- i.e. an UN-time-bounded SUM over the ENTIRE usage_logs table (2.4M rows / 2.3 GB
-- on prod) on EVERY GroupsView load, with no cache. The cumulative all-time total
-- cannot be served by any windowed rollup, so this table pre-aggregates the
-- per-group cost per server-TZ day; the read path sums the completed days from
-- here and reads only TODAY's partial day live from raw usage_logs (a narrow
-- created_at window the index serves cheaply). See
-- usage_log_repo_tk_group_rollup.go (read) and dashboard_aggregation_repo_tk_group.go
-- (feeder + one-time backfill).
--
-- Grain: one row per (group_id, bucket_date). Metric: actual_cost only — that is
-- the single column the usage-summary reads. "platform"/token columns are
-- deliberately omitted (YAGNI); add later if a consumer needs them.
--
-- Retention: UNLIKE the windowed usage_dashboard_* rollups, this table is NOT
-- pruned by CleanupAggregates — the Groups summary is an ALL-TIME cumulative, so
-- dropping old days would understate the total. The table is tiny (active groups
-- x days; O(10^4-10^5) rows over years) so keeping it indefinitely is cheap.
--
-- Population: rows are inserted in the configured server timezone by
-- DashboardAggregationService (watermark-driven, ~1 min) plus a one-time
-- historical backfill at first aggregation cycle. The bucket_date timezone is
-- intentionally NOT computed here: a static migration cannot read the runtime
-- timezone, and bucketing the backfill in the wrong TZ would mis-split the
-- today/history boundary at read time. The Go feeder uses timezone.Name() so the
-- grain matches the read path exactly.

CREATE TABLE IF NOT EXISTS usage_dashboard_group_daily (
    bucket_date  DATE NOT NULL,
    group_id     BIGINT NOT NULL,
    actual_cost  DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_date, group_id)
);

-- The read path sums actual_cost grouped by group_id over bucket_date < today.
-- A (group_id, bucket_date) index serves that per-group range aggregation; the
-- PK's leading bucket_date serves the cross-group "all completed days" scan.
CREATE INDEX IF NOT EXISTS idx_usage_dashboard_group_daily_group
    ON usage_dashboard_group_daily (group_id, bucket_date);
