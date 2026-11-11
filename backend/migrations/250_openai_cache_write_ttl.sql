-- bluegreen-safe-destructive-ok: expand-only constant DEFAULT 0 columns; old
-- writers may omit them and old readers ignore them. PostgreSQL 18.6 was read-only
-- verified on both data hosts on 2026-11-11; row counts and rollback analysis are
-- in docs/ops/reports/upstream-merge-implementation-20261111.md. No table rewrite;
-- bounded lock acquisition fails atomically. Application rollback retains columns.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- OpenAI cache-write lifetimes are separate from Anthropic 5m/1h buckets.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS cache_creation_30m_tokens INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_creation_12h_tokens INTEGER NOT NULL DEFAULT 0;

ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS cache_write_12h_price NUMERIC(20,12);
COMMENT ON COLUMN channel_model_pricing.cache_write_12h_price IS
    'OpenAI 12h cache write price per token; NULL preserves the generic cache_write_price';

ALTER TABLE channel_pricing_intervals
    ADD COLUMN IF NOT EXISTS cache_write_12h_price NUMERIC(20,12);
COMMENT ON COLUMN channel_pricing_intervals.cache_write_12h_price IS
    'OpenAI 12h cache write price per token; NULL preserves the generic cache_write_price';

ALTER TABLE channel_account_stats_model_pricing
    ADD COLUMN IF NOT EXISTS cache_write_12h_price NUMERIC(20,12);
COMMENT ON COLUMN channel_account_stats_model_pricing.cache_write_12h_price IS
    'OpenAI 12h cache write price per token; NULL preserves the generic cache_write_price';

ALTER TABLE channel_account_stats_pricing_intervals
    ADD COLUMN IF NOT EXISTS cache_write_12h_price NUMERIC(20,12);
COMMENT ON COLUMN channel_account_stats_pricing_intervals.cache_write_12h_price IS
    'OpenAI 12h cache write price per token; NULL preserves the generic cache_write_price';
