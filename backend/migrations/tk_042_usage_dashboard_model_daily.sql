-- TK-only per-(requested-model, day) rollup for the admin dashboard model-stats
-- widget. GetModelStatsWithFilters (requested source, no dimension filters) previously
-- scanned the full window of usage_logs; this table serves completed days from
-- pre-aggregated rows plus raw usage_logs for partial/today slices (same
-- rollupMiddle/rawHead/rawTail decomposition as tk_034).

CREATE TABLE IF NOT EXISTS usage_dashboard_model_daily (
    bucket_date           DATE NOT NULL,
    model                 TEXT NOT NULL,
    total_requests        BIGINT NOT NULL DEFAULT 0,
    input_tokens          BIGINT NOT NULL DEFAULT 0,
    output_tokens         BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens     BIGINT NOT NULL DEFAULT 0,
    total_cost            DECIMAL(20, 10) NOT NULL DEFAULT 0,
    actual_cost           DECIMAL(20, 10) NOT NULL DEFAULT 0,
    account_cost          DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_date, model)
);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_model_daily_date
    ON usage_dashboard_model_daily (bucket_date DESC);
