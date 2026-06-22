-- Migration: tk_042_qwen_turbo_model_mapping
--
-- Advertise qwen-turbo on the Qwen account (id=60) so the Qwen group can access it.
--
-- Why (Goal 2 fleet servable-expansion, 2026-06-22): qwen-turbo is in the ct=17
-- Ali/DashScope adaptor catalog and DashScope serves it with account 60's key, but
-- TK returned 429 (empty pool / not_allowlisted) because account 60's
-- credentials.model_mapping is an identity WHITELIST that did not list qwen-turbo.
-- Merging it makes the newapi bridge advertise + route it. Reachability confirmed by
-- post-apply livefire (the gateway empty-pool gate makes a pre-map probe impossible).
--
-- Pricing ships in the SAME release via backend/internal/service/tk_pricing_overlay.json
-- (qwen-turbo, flat non-thinking China-mainland list; compile-embedded), so the
-- whitelist add and the price go live together — no $0 billing window.
--
-- Merge (jsonb ||) preserves the existing qwen3.7-max/qwen-plus/etc. entries; only the
-- qwen-turbo identity key is added. Raw-SQL account mutation bypasses the Ent
-- snapshot-refresh hooks, so enqueue a scheduler_outbox account_changed event
-- (pattern: tk_024/tk_022).
--
-- Idempotent + cross-deployment safe: guarded by (id, name, platform='newapi',
-- channel_type=17) so re-running merges the same key (no-op) and a bare id colliding
-- with an unrelated account in another DB cannot match.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH upd AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "qwen-turbo": "qwen-turbo"
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
SELECT 'account_changed', id, NULL, NULL FROM upd;
