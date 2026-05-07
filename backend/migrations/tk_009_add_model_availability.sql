-- TK migration 009: per-(platform, model) availability state for /pricing single source of truth
--
-- Populated via 1-line gateway hook (gateway_service.go recordUsageCore tail) and 3 handler taps;
-- consumed by pricing_catalog_tk.go BuildPublicCatalog to expose `availability` field on each
-- catalog model entry. See docs/approved/pricing-availability-source-of-truth.md.
--
-- Also: extends channel_monitors with `kind` (user vs system_availability) so the existing
-- ChannelMonitorRunner can serve as the active-probe backstop without a new scheduler.

CREATE TABLE IF NOT EXISTS model_availability (
    id BIGSERIAL PRIMARY KEY,
    platform VARCHAR(20) NOT NULL,
    model_id VARCHAR(200) NOT NULL,
    status VARCHAR(15) NOT NULL DEFAULT 'untested',
    last_seen_ok_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    last_failure_kind VARCHAR(50) NOT NULL DEFAULT '',
    upstream_status_code_last INT,
    last_checked_at TIMESTAMPTZ,
    sample_ok_24h INT NOT NULL DEFAULT 0,
    sample_total_24h INT NOT NULL DEFAULT 0,
    rolling_window_started_at TIMESTAMPTZ,
    last_account_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT model_availability_status_check
        CHECK (status IN ('ok', 'stale', 'unreachable', 'untested')),
    CONSTRAINT model_availability_platform_check
        CHECK (platform IN ('openai', 'anthropic', 'gemini', 'antigravity', 'newapi'))
);

-- Primary lookup: catalog handler joins on (platform, model_id).
CREATE UNIQUE INDEX IF NOT EXISTS uq_model_availability_platform_model
    ON model_availability (platform, model_id);

-- Seeder selection: needs_probe = (last_checked_at < now-24h) AND sample_total_24h=0;
-- index supports the ORDER BY last_checked_at ASC NULLS FIRST tail of that query.
CREATE INDEX IF NOT EXISTS idx_model_availability_status_checked
    ON model_availability (status, last_checked_at);

-- channel_monitors discriminator: existing rows default to 'user' (no behavior change).
-- pricing_availability_seeder_tk.go inserts/maintains 'system_availability' rows.
ALTER TABLE channel_monitors
    ADD COLUMN IF NOT EXISTS kind VARCHAR(24) NOT NULL DEFAULT 'user',
    ADD COLUMN IF NOT EXISTS seed_source VARCHAR(64) NOT NULL DEFAULT '';

-- Seeder needs to enumerate kind=system_availability rows efficiently.
CREATE INDEX IF NOT EXISTS idx_channel_monitors_kind ON channel_monitors (kind);
