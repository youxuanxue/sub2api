-- Migration: tk_012_create_tiers_and_account_tier_id
-- Introduce the `tiers` reference table (l1..l5) + accounts.tier_id binding so
-- anthropic OAuth accounts reference per-tier config by id (mirrors the
-- tls_fingerprint_profiles reference model) instead of having values copied onto
-- each account. The git baseline JSON (backend/internal/baseline +
-- deploy/aws/stage0, sentinel-locked) stays the single source of truth; this
-- table is its projection. TierService re-asserts l1..l5 from the embedded
-- baseline on every startup (ensureSeededFromBaseline), and the ops/anthropic
-- pipeline re-asserts fleet-wide — so the hand-seeded values below are only the
-- pre-startup bootstrap (kept correct for a fresh DB before the app boots).

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS tiers (
    id                           BIGSERIAL    PRIMARY KEY,
    name                         VARCHAR(100) NOT NULL UNIQUE,
    description                  TEXT,
    concurrency                  INT          NOT NULL DEFAULT 3,
    priority                     INT          NOT NULL DEFAULT 50,
    rate_multiplier              DECIMAL(10,4) NOT NULL DEFAULT 1.0,
    base_rpm                     INT          NOT NULL DEFAULT 0,
    max_sessions                 INT          NOT NULL DEFAULT 0,
    rpm_sticky_buffer            INT          NOT NULL DEFAULT 0,
    session_idle_timeout_minutes INT          NOT NULL DEFAULT 8,
    window_cost_limit            DECIMAL(10,4) NOT NULL DEFAULT 0,
    window_cost_sticky_reserve   DECIMAL(10,4) NOT NULL DEFAULT 0,
    cache_ttl_override_enabled   BOOLEAN      NOT NULL DEFAULT false,
    cache_ttl_override_target    VARCHAR(20),
    tls_profile_name             VARCHAR(100),
    tls_profile_id               BIGINT,
    created_at                   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at                   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE tiers IS 'TokenKey anthropic-oauth stability tiers (l1..l5). Projection of the git baseline JSON; referenced by accounts.tier_id.';
COMMENT ON COLUMN tiers.concurrency IS 'oauth account concurrency write-source (value-synced onto account.concurrency by the reconciler/apply).';
COMMENT ON COLUMN tiers.priority IS 'Projection only — accounts.priority is owned by the window-rebalance pipeline, never pushed from here.';

-- accounts.tier_id binding (nullable; only anthropic OAuth accounts use it).
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS tier_id BIGINT;
COMMENT ON COLUMN accounts.tier_id IS 'TK: bound anthropic-oauth stability tier id (tiers table).';
CREATE INDEX IF NOT EXISTS ix_accounts_tier_id ON accounts (tier_id) WHERE deleted_at IS NULL;

-- Bootstrap seed l1..l5 (effective = shared_baseline.extra overlaid with the
-- per-tier baseline, from anthropic-oauth-stability-baselines-tiered.json).
-- ON CONFLICT DO NOTHING: startup ensureSeededFromBaseline owns ongoing
-- re-assertion of the values from the embedded JSON.
--
-- Values below MUST equal the JSON-derived effective baseline: concurrency /
-- priority / rate_multiplier come from tiers[id].baseline.account; base_rpm /
-- max_sessions / rpm_sticky_buffer / session_idle_timeout_minutes /
-- window_cost_limit / window_cost_sticky_reserve / cache_ttl_override_* come
-- from shared_baseline.extra overlaid with tiers[id].baseline.extra. The
-- scripts/sentinels/check-tier-baseline-embed.py sentinel asserts this seed ==
-- the embedded JSON effective values, so a JSON edit not mirrored here is a hard
-- preflight/CI failure (plan risk #6 — tiers-table-vs-git projection drift).
INSERT INTO tiers (name, concurrency, priority, rate_multiplier, base_rpm, max_sessions, rpm_sticky_buffer, session_idle_timeout_minutes, window_cost_limit, window_cost_sticky_reserve, cache_ttl_override_enabled, cache_ttl_override_target, tls_profile_name)
VALUES
    ('l1',  4, 1, 1.0, 21,  45,  8, 8, 800, 0, true, '1h', 'tk_canonical_cc_oauth'),
    ('l2',  6, 2, 1.0, 42,  90, 15, 8, 800, 0, true, '1h', 'tk_canonical_cc_oauth'),
    ('l3',  8, 3, 1.0, 63, 120, 23, 8, 800, 0, true, '1h', 'tk_canonical_cc_oauth'),
    ('l4', 10, 4, 1.0, 84, 150, 30, 8, 800, 0, true, '1h', 'tk_canonical_cc_oauth'),
    ('l5', 12, 5, 1.0, 84, 180, 30, 8, 800, 0, true, '1h', 'tk_canonical_cc_oauth')
ON CONFLICT (name) DO NOTHING;

-- Backfill tier_id for existing anthropic OAuth accounts that carry the legacy
-- extra.stability_tier label. Idempotent (only fills NULL tier_id). The legacy
-- extra.* values are intentionally left in place (no runtime gap; the runtime
-- resolver overlays tier values, and the reconciler re-asserts concurrency).
UPDATE accounts a
SET tier_id = t.id, updated_at = NOW()
FROM tiers t
WHERE a.platform = 'anthropic'
  AND a.type = 'oauth'
  AND a.deleted_at IS NULL
  AND a.tier_id IS NULL
  AND lower(trim(a.extra->>'stability_tier')) = t.name;
