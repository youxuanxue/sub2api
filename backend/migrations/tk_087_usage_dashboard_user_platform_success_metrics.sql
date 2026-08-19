-- Keep the all-request fields used by admin spending ranking, while adding
-- billed-success metrics for dashboard platform cards. A single daily rollup
-- row can contain both failed zero-cost placeholders and successful requests,
-- so filtering the row by actual_cost is not sufficient.
-- bluegreen-safe-destructive-ok: these additive columns use IF NOT EXISTS and
-- DEFAULT 0, so old and new app binaries can read the same table during rollout.
ALTER TABLE usage_dashboard_user_platform_daily
    ADD COLUMN IF NOT EXISTS successful_requests BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS successful_input_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS successful_output_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS successful_cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS successful_cache_read_tokens BIGINT NOT NULL DEFAULT 0;

-- Historical rows are rebuilt by DashboardAggregationService using the runtime
-- reporting timezone. Migration sessions are pinned to UTC for partition safety,
-- so doing the backfill here would assign local-midnight events to the wrong day.
CREATE TABLE IF NOT EXISTS usage_dashboard_user_platform_rollup_state (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    success_metrics_version INTEGER NOT NULL DEFAULT 0,
    timezone_name VARCHAR(100) NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO usage_dashboard_user_platform_rollup_state (id)
VALUES (1)
ON CONFLICT (id) DO NOTHING;
