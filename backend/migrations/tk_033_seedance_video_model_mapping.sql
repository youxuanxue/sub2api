-- Migration: tk_033_seedance_video_model_mapping
--
-- Advertise the doubao-seedance VIDEO models on the volcengine account (id=7),
-- so the volcengine group (and the china group it also belongs to) can serve
-- async video generation (/v1/video/generations):
--   - account 7 "volcengine" (VolcEngine Ark, channel_type=45):
--       doubao-seedance-1-0-pro-250528
--       doubao-seedance-1-5-pro-251215
--       doubao-seedance-2-0-260128
--       doubao-seedance-2-0-fast-260128
--
-- Why (prod 2026-06-19, vol-test investigation): account 7's
-- credentials.model_mapping was an identity WHITELIST listing ONLY chat/LLM
-- models (glm-4-7, doubao-seed-* chat, doubao-1-5-* chat). A non-empty
-- model_mapping is a strict allowlist (service.Account.IsModelSupported): the
-- OpenAI-compat scheduler's candidate filter
-- (openai_account_scheduler.go isAccountRequestCompatible -> IsModelSupported)
-- dropped account 7 for any seedance video request, emptying the single-account
-- pool. That surfaced as HTTP 400 "Unsupported model: doubao-seedance-..."
-- (openAICompatNoCandidateError -> ErrUnsupportedModel), so seedance video could
-- never be generated through the vol-test key even though the account is a
-- correctly-configured VolcEngine Ark video channel (platform=newapi,
-- channel_type=45, base_url=https://ark.cn-beijing.volces.com, api_key set).
--
-- Merging the four seedance ids into the whitelist (identity mapping) makes the
-- bridge advertise + route them to VolcEngine Ark; the request then passes the
-- scheduler and is dispatched via the new-api doubao task adaptor.
--
-- Pricing already shipped in backend/internal/service/tk_pricing_overlay.json
-- (each model carries output_cost_per_second from the VolcEngine Ark official
-- model-pricing page; per-second video billing uses output_cost_per_second).
-- The overlay is compile-embedded, so the whitelist add lands on an image that
-- already prices these names — no $0 / unpriced-400 window for the four ids.
-- (The seedance *lite-t2v / *lite-i2v variants are deliberately NOT priced and
-- NOT added here — they would hit the unpriced-media 400 until priced.)
--
-- Merge (jsonb ||) preserves the existing chat identity-whitelist entries; only
-- the four new identity keys are added, so the chat models the account already
-- serves keep working. Raw-SQL account mutation bypasses the Ent
-- snapshot-refresh hooks, so enqueue a scheduler_outbox account_changed event
-- (pattern: tk_022 / tk_024 / tk_027 / tk_029 / tk_032) so the running scheduler
-- sees the change without a restart.
--
-- Idempotent + cross-deployment safe: guarded by (id, name, platform='newapi',
-- channel_type=45) so re-running merges the same keys (no-op) and a bare id
-- colliding with an unrelated account in another DB cannot match.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- volcengine account 7: add the four priced doubao-seedance VIDEO models.
WITH upd_volcengine AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "doubao-seedance-1-0-pro-250528": "doubao-seedance-1-0-pro-250528",
                "doubao-seedance-1-5-pro-251215": "doubao-seedance-1-5-pro-251215",
                "doubao-seedance-2-0-260128": "doubao-seedance-2-0-260128",
                "doubao-seedance-2-0-fast-260128": "doubao-seedance-2-0-fast-260128"
            }'::jsonb
        ),
        updated_at = NOW()
    WHERE id = 7
      AND name = 'volcengine'
      AND platform = 'newapi'
      AND channel_type = 45
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd_volcengine;
