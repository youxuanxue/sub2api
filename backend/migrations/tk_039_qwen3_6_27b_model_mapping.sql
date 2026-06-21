-- Migration: tk_039_qwen3_6_27b_model_mapping
--
-- Advertise the dense open-source Qwen3.6 27B model on the Qwen account (id=60),
-- so the Qwen group can serve it:
--   - account 60 "Qwen" (Ali, channel_type=17): qwen3.6-27b
--
-- Why (prod 2026-06-21, per customer request): a client asked for the dense 27B
-- Qwen3.6 model (released 2026-04-22; DashScope model id "qwen3.6-27b"). account
-- 60's credentials.model_mapping is an identity WHITELIST that listed the qwen3.7-max
-- family + qwen-max/qwen-plus (tk_027), qwen3.7-plus / qwen3.6-flash /
-- qwen3-coder-plus (tk_024), the dense qwen3-8b/14b/32b (tk_029) and the flagship
-- qwen3-235b-a22b (tk_032), but NOT qwen3.6-27b. The scheduler found no account
-- advertising this name for the group -> empty pool -> fast-fail 429. DashScope
-- serves qwen3.6-27b directly with account 60's key (China-mainland/Beijing
-- endpoint), so merging it into the whitelist makes the bridge advertise + route it.
--
-- Pricing shipped alongside this migration (added 2026-06-21) via
-- backend/internal/service/tk_pricing_overlay.json (qwen3.6-27b: in ¥3/M, out
-- 非思考 ¥18/M / 思考 ¥18/M — 非思考和思考同价; all ÷6.7; thinking-rate default because
-- open-source dense Qwen3 defaults enable_thinking=true). The overlay is
-- compile-embedded, so the whitelist add lands on an image that already prices this
-- name -- no $0 window.
--
-- Merge (jsonb ||) preserves the existing identity-whitelist entries; only the one
-- new identity key is added. Raw-SQL account mutation bypasses the Ent
-- snapshot-refresh hooks, so enqueue a scheduler_outbox account_changed event
-- (pattern: tk_022 / tk_024 / tk_027 / tk_029 / tk_032) so the running scheduler
-- sees the change without a restart.
--
-- Idempotent + cross-deployment safe: guarded by (id, name, platform='newapi',
-- channel_type=17) so re-running merges the same key (no-op) and a bare id
-- colliding with an unrelated account in another DB cannot match.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- Qwen account 60: add qwen3.6-27b (dense, open-source, hybrid thinking).
WITH upd_qwen AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "qwen3.6-27b": "qwen3.6-27b"
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
