-- Migration: tk_020_volcengine_servable_refresh
--
-- Refresh the VolcEngine newapi channel (account id=7, platform=newapi,
-- channel_type=45) advertised model set, and re-enable it.
--
-- Background / incident:
--   account 7 "volcengine" advertised ~70 stale model IDs in
--   credentials.model_mapping. Almost all were either never 开通 (activated) on
--   the underlying VolcEngine Ark account or already 下线 (retired), so every
--   client request returned ark `404 InvalidEndpointOrModel.NotFound`. The
--   newapi bridge swallows that upstream status code and wraps it as a bare
--   502 (ops_error_logs: error_phase=upstream, upstream_status_code=null),
--   which spiked prod upstream_error_rate to ~30% and fired a P0 (2026-06-10).
--   The account was manually set schedulable=false to stop the bleeding.
--
-- What was done:
--   Probed VolcEngine Ark directly (DB base_url+api_key, bypassing the TK
--   scheduling pool) for every model in ark /api/v3/models. 36 returned
--   200/400 (servable), 83 returned a stable 404 (not 开通 / 下线). Re-tested
--   for stability (zero transient).
--
-- Scope of THIS migration (batch 1 = 20 chat/text models, all priced):
--   The 20 IDs below are the empirically-servable chat/text doubao + glm models,
--   each given an official VolcEngine Ark price in tk_pricing_overlay.json
--   (≤32K input tier, CNY/USD=7.3) so they bill non-zero (no #688
--   pricing_missing). model_mapping is an identity whitelist (key===value); the
--   keys are what this newapi channel advertises into the gateway and "My Menu".
--
-- Deliberately EXCLUDED (handled elsewhere / later):
--   - deepseek-v4-flash/pro, deepseek-v3-2: served via the official DeepSeek
--     direct channel (group 11, api.deepseek.com) at official rates. VolcEngine's
--     own deepseek-v4-pro list price (¥12/¥24) is ~4x official, so serving it via
--     this VolcEngine channel and billing at official rates would lose money.
--   - embedding (doubao-embedding-vision) + media (seedream image, seedance
--     video, 3D) — batch 2: embedding has dual text/image input pricing and media
--     bills via RunImageRelay (per-image) / CalculateVideoCost (per-second), which
--     need separate overlay fields + per-second rates.
--
-- Re-enable: this migration flips schedulable back to true. Prices ship in the
-- same release (tk_pricing_overlay.json is a compile-time embed), so the 20
-- models are priced the moment they become schedulable.
--
-- Raw-SQL account mutations bypass the Ent hooks that enqueue a scheduler
-- snapshot refresh, so enqueue one scheduler_outbox `account_changed` event
-- (same shape as tk_015) — otherwise running replicas keep the stale model_mapping
-- until their next full snapshot reload.
--
-- Idempotent: re-running overwrites model_mapping with the same map, sets
-- schedulable=true (already true), and enqueues one more refresh event.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH upd AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            '{
                "doubao-seed-2-0-pro-260215": "doubao-seed-2-0-pro-260215",
                "doubao-seed-2-0-code-preview-260215": "doubao-seed-2-0-code-preview-260215",
                "doubao-seed-2-0-lite-260215": "doubao-seed-2-0-lite-260215",
                "doubao-seed-2-0-lite-260428": "doubao-seed-2-0-lite-260428",
                "doubao-seed-2-0-mini-260215": "doubao-seed-2-0-mini-260215",
                "doubao-seed-2-0-mini-260428": "doubao-seed-2-0-mini-260428",
                "doubao-seed-1-8-251228": "doubao-seed-1-8-251228",
                "doubao-seed-1-6-250615": "doubao-seed-1-6-250615",
                "doubao-seed-1-6-251015": "doubao-seed-1-6-251015",
                "doubao-seed-1-6-flash-250615": "doubao-seed-1-6-flash-250615",
                "doubao-seed-1-6-flash-250828": "doubao-seed-1-6-flash-250828",
                "doubao-seed-1-6-vision-250815": "doubao-seed-1-6-vision-250815",
                "doubao-seed-character-251128": "doubao-seed-character-251128",
                "doubao-seed-code-preview-251028": "doubao-seed-code-preview-251028",
                "doubao-1-5-pro-32k-250115": "doubao-1-5-pro-32k-250115",
                "doubao-1-5-pro-32k-character-250715": "doubao-1-5-pro-32k-character-250715",
                "doubao-1-5-lite-32k-250115": "doubao-1-5-lite-32k-250115",
                "doubao-1-5-vision-pro-32k-250115": "doubao-1-5-vision-pro-32k-250115",
                "doubao-seed-translation-250915": "doubao-seed-translation-250915",
                "glm-4-7-251222": "glm-4-7-251222"
            }'::jsonb
        ),
        schedulable = true,
        updated_at = NOW()
    WHERE id = 7
      AND platform = 'newapi'
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
