-- Migration: tk_032_qwen3_235b_a22b_model_mapping
--
-- Advertise the flagship open-source Qwen3 MoE model on the Qwen account (id=60),
-- so the Qwen group can serve it:
--   - account 60 "Qwen" (Ali, channel_type=17): qwen3-235b-a22b
--
-- Why (prod 2026-06-18, per customer request): the Qwen group previously listed
-- only the smaller Qwen3 models (qwen3.7-max family + qwen-max/qwen-plus via
-- tk_027, qwen3.7-plus / qwen3.6-flash / qwen3-coder-plus via tk_024, and the
-- dense qwen3-8b/14b/32b via tk_029). A client asked for the flagship
-- qwen3-235b-a22b (235B total / 22B active MoE). account 60's
-- credentials.model_mapping is an identity WHITELIST that did not list this name,
-- so the scheduler found no account advertising it for the group -> empty pool ->
-- fast-fail 429. DashScope serves qwen3-235b-a22b directly with account 60's key
-- (China-mainland/Beijing endpoint), so merging it into the whitelist makes the
-- bridge advertise + route it.
--
-- Pricing shipped alongside this migration (added 2026-06-18) via
-- backend/internal/service/tk_pricing_overlay.json (qwen3-235b-a22b: in ¥2/M, out
-- 非思考 ¥8/M / 思考 ¥20/M; all ÷6.7; coincidentally the same per-token rates as
-- qwen3-32b; thinking-rate default because the open-source Qwen3 hybrid base alias
-- defaults enable_thinking=true). The overlay is compile-embedded, so the
-- whitelist add lands on an image that already prices this name -- no $0 window.
--
-- Merge (jsonb ||) preserves the existing identity-whitelist entries; only the one
-- new identity key is added. Raw-SQL account mutation bypasses the Ent
-- snapshot-refresh hooks, so enqueue a scheduler_outbox account_changed event
-- (pattern: tk_022 / tk_024 / tk_027 / tk_029) so the running scheduler sees the
-- change without a restart.
--
-- Idempotent + cross-deployment safe: guarded by (id, name, platform='newapi',
-- channel_type=17) so re-running merges the same key (no-op) and a bare id
-- colliding with an unrelated account in another DB cannot match.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- Qwen account 60: add qwen3-235b-a22b (open-source MoE, hybrid thinking).
WITH upd_qwen AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "qwen3-235b-a22b": "qwen3-235b-a22b"
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
