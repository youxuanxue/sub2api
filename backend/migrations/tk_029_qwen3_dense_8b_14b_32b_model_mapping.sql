-- Migration: tk_029_qwen3_dense_8b_14b_32b_model_mapping
--
-- Advertise the three open-source DENSE Qwen3 models on the Qwen account (id=60),
-- so the Qwen group can serve them:
--   - account 60 "Qwen" (Ali, channel_type=17): qwen3-8b, qwen3-14b, qwen3-32b
--
-- Why (prod 2026-06-17, per customer request): clients sent these dense Qwen3
-- names but account 60's credentials.model_mapping is an identity WHITELIST that
-- only listed the qwen3.7-max family + qwen-max/qwen-plus + qwen3.7-plus /
-- qwen3.6-flash / qwen3-coder-plus (tk_024) and the legacy aliases (tk_027). The
-- scheduler found no account advertising the dense names for the group -> empty
-- pool -> fast-fail 429. DashScope serves these names directly with account 60's
-- key (China-mainland/Beijing endpoint), so merging them into the whitelist makes
-- the bridge advertise + route them.
--
-- Pricing already shipped in #812 (added 2026-06-17) via
-- backend/internal/service/tk_pricing_overlay.json (qwen3-8b: in ¥0.5/M, out
-- 非思考 ¥2/M / 思考 ¥5/M; qwen3-14b: in ¥1/M, out ¥4/M / ¥10/M; qwen3-32b: in ¥2/M,
-- out ¥8/M / ¥20/M; all ÷6.7; thinking-rate default because open-source dense
-- Qwen3 defaults enable_thinking=true). The overlay is compile-embedded, so the
-- whitelist add lands on an image that already prices these names -- no $0 window.
--
-- Merge (jsonb ||) preserves the existing identity-whitelist entries; only the
-- three new identity keys are added. Raw-SQL account mutation bypasses the Ent
-- snapshot-refresh hooks, so enqueue a scheduler_outbox account_changed event
-- (pattern: tk_022 / tk_024 / tk_027) so the running scheduler sees the change
-- without a restart.
--
-- Idempotent + cross-deployment safe: guarded by (id, name, platform='newapi',
-- channel_type=17) so re-running merges the same keys (no-op) and a bare id
-- colliding with an unrelated account in another DB cannot match.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- Qwen account 60: add qwen3-8b, qwen3-14b, qwen3-32b (dense, open-source).
WITH upd_qwen AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "qwen3-8b": "qwen3-8b",
                "qwen3-14b": "qwen3-14b",
                "qwen3-32b": "qwen3-32b"
            }'::jsonb
        ),
        updated_at = NOW()
    WHERE id = 60
      AND name = 'Qwen'
      AND platform = 'newapi'
      AND channel_type = 17
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd_qwen;
