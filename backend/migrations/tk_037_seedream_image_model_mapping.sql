-- Migration: tk_037_seedream_image_model_mapping
--
-- Advertise the doubao-seedream IMAGE model on the volcengine account (id=7),
-- so the volcengine/newapi compat pool can serve synchronous image generation
-- (/v1/images/generations) through the new-api volcengine adaptor:
--   - account 7 "volcengine" (VolcEngine Ark, channel_type=45):
--       doubao-seedream-4-0-250828
--
-- Why (prod 2026-06-20, vol-test investigation): a universal-key request for
-- POST /v1/images/generations model "doubao-seedream-4-0-250828" returned
-- HTTP 429 "No available accounts" (ops_error_logs phase=routing). Root cause:
-- account 7's credentials.model_mapping is a strict identity WHITELIST that
-- listed only chat/LLM ids (glm-4-7, doubao-seed-* chat, doubao-1-5-*) and the
-- four seedance VIDEO ids (tk_033) — but NO seedream IMAGE id. A non-empty
-- model_mapping is an allowlist (service.Account.IsModelSupported): the
-- universal-key resolver (which since #887 converges to the set of models a
-- group actually serves) + the OpenAI-compat scheduler candidate filter
-- (openai_account_scheduler.go isAccountRequestCompatible -> IsModelSupported)
-- both dropped account 7 for the seedream request, emptying the single-account
-- pool and surfacing as a 429 empty-pool error. The account is otherwise a
-- correctly-configured VolcEngine Ark image channel (platform=newapi,
-- channel_type=45, base_url=https://ark.cn-beijing.volces.com, api_key set);
-- a direct Ark data-plane activation probe confirmed the api_key serves
-- doubao-seedream-4-0-250828 (HTTP 200).
--
-- ONLY the prefixed id is mapped. The new-api volcengine ModelList
-- (relay/channel/volcengine/constants.go) and tk_pricing_overlay.json both
-- carry a no-prefix alias "seedream-4-0-250828", but the Ark data-plane
-- activation probe returned HTTP 404 (unsupported) for the no-prefix form on
-- account 7's upstream. Advertising a 404 id in the allowlist would route real
-- requests to an upstream that rejects them, so the alias is deliberately NOT
-- added to the model_mapping (it remains overlay-priced purely for billing-key
-- parity in case the relay ever rewrites to it; it is not a served name here).
--
-- Pricing already shipped in backend/internal/service/tk_pricing_overlay.json
-- (doubao-seedream-4-0-250828 carries output_cost_per_image from the VolcEngine
-- Ark official model-pricing page, 0.2 CNY/image / 6.7 = 0.0298507 USD/image).
-- The overlay is compile-embedded, so the whitelist add lands on an image that
-- already prices the id — no $0 / unpriced-400 window.
--
-- Merge (jsonb ||) preserves the existing chat + seedance identity-whitelist
-- entries; only the one new identity key is added, so everything the account
-- already serves keeps working. Raw-SQL account mutation bypasses the Ent
-- snapshot-refresh hooks, so enqueue a scheduler_outbox account_changed event
-- (pattern: tk_022 / tk_024 / tk_027 / tk_029 / tk_032 / tk_033) so the running
-- scheduler sees the change without a restart.
--
-- Idempotent + cross-deployment safe: guarded by (id, name, platform='newapi',
-- channel_type=45) so re-running merges the same key (no-op) and a bare id
-- colliding with an unrelated account in another DB cannot match.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- volcengine account 7: add the priced doubao-seedream IMAGE model.
WITH upd_volcengine AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "doubao-seedream-4-0-250828": "doubao-seedream-4-0-250828"
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
