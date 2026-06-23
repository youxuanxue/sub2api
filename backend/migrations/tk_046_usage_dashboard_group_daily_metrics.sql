-- Extend the TK per-(group, day) rollup beyond the Groups page cost summary so
-- it can also serve the admin Dashboard/Usage group distribution chart.
--
-- Existing deployments may already have usage_dashboard_group_daily with only
-- actual_cost. The Go feeder writes these metrics for new/recomputed days, and
-- backfillGroupDailyMetricsAllOnce fills historical rows before the read path
-- uses them.

ALTER TABLE usage_dashboard_group_daily
    ADD COLUMN IF NOT EXISTS total_requests BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS input_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS output_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS total_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0;
