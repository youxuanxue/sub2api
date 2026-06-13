-- Migration: tk_027_deepseek_qwen_legacy_alias_model_mapping
--
-- Advertise the upstream-canonical/legacy model names on the DeepSeek (id=39) and
-- Qwen (id=60) newapi accounts so their groups can serve them:
--   - account 39 "ds-官"  (DeepSeek, channel_type=43): deepseek-chat, deepseek-reasoner
--   - account 60 "Qwen"   (Ali,      channel_type=17): qwen-max, qwen-plus
--
-- Why (prod 2026-06-13, 05:44 UTC): clients sent these upstream-canonical names but
-- each account's credentials.model_mapping is an identity WHITELIST that only listed
-- the renamed lines (deepseek-v4-*; qwen3.7-* / qwen3.6-flash / qwen3-coder-plus). The
-- scheduler found no account advertising the requested names for the group -> empty
-- pool -> fast-fail. DashScope/DeepSeek serve these names directly with each account's
-- key, so merging them into the whitelist makes the bridge advertise + route them.
-- (PR #753 separately made an unservable-model name return a client 400 instead of an
-- empty-pool 429; this migration is the other half — actually serving the names.)
--
-- Pricing already shipped in v1.7.100 via backend/internal/service/tk_pricing_overlay.json
-- (deepseek-chat/reasoner: litellm base mirror; qwen-max: flat ¥2.4/¥9.6÷6.7; qwen-plus:
-- tiered ¥0.8·2/¥2.4·20/¥4.8·48÷6.7, non-thinking list — the qwen-plus series defaults
-- enable_thinking=false). The overlay is compile-embedded and prod already runs 1.7.100,
-- so the whitelist add lands on an image that already prices these names — no $0 window.
--
-- Merge (jsonb ||) preserves each account's existing identity-whitelist entries; only the
-- new identity keys are added. Raw-SQL account mutation bypasses the Ent snapshot-refresh
-- hooks, so enqueue a scheduler_outbox account_changed event (pattern: tk_022 / tk_024).
--
-- Idempotent + cross-deployment safe: guarded by (id, name, platform='newapi',
-- channel_type) so re-running merges the same keys (no-op) and a bare id colliding with
-- an unrelated account in another DB cannot match.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- DeepSeek account 39: add deepseek-chat, deepseek-reasoner.
WITH upd_ds AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "deepseek-chat": "deepseek-chat",
                "deepseek-reasoner": "deepseek-reasoner"
            }'::jsonb
        ),
        updated_at = NOW()
    WHERE id = 39
      AND name = 'ds-官'
      AND platform = 'newapi'
      AND channel_type = 43
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd_ds;

-- Qwen account 60: add qwen-max, qwen-plus.
WITH upd_qwen AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "qwen-max": "qwen-max",
                "qwen-plus": "qwen-plus"
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
