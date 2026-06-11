-- Migration: tk_024_qwen_plus_flash_coder_model_mapping
--
-- Advertise three more DashScope models on the Qwen account (id=60), so the Qwen
-- group can access them: qwen3.7-plus, qwen3.6-flash, qwen3-coder-plus.
--
-- Why (found during model-access probe 2026-06-11): DashScope serves all three
-- with account 60's key (direct probe = 200), but TK returned 503 for them because
-- account 60's credentials.model_mapping is an identity WHITELIST that only listed
-- the qwen3.7-max family. The scheduler found no account advertising these models
-- for the group -> empty pool -> 503. Merging them into the whitelist makes the
-- bridge advertise + route them.
--
-- Pricing ships in the SAME release via backend/internal/service/tk_pricing_overlay.json
-- (interval/tiered pricing — these models are tiered by input-token count; see the
-- overlay-interval support added in this PR). The overlay is compile-embedded, so the
-- whitelist add and the prices go live together — no $0 billing window.
--
-- Merge (jsonb ||) preserves the existing qwen3.7-max family entries; only the three
-- new identity keys are added. Raw-SQL account mutation bypasses the Ent snapshot-refresh
-- hooks, so enqueue a scheduler_outbox account_changed event (pattern: tk_022).
--
-- Idempotent + cross-deployment safe: guarded by (id, name, platform='newapi',
-- channel_type=17) so re-running merges the same keys (no-op) and a bare id colliding
-- with an unrelated account in another DB cannot match.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH upd AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "qwen3.7-plus": "qwen3.7-plus",
                "qwen3.6-flash": "qwen3.6-flash",
                "qwen3-coder-plus": "qwen3-coder-plus"
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
