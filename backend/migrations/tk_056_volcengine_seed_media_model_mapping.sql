-- Migration: tk_056_volcengine_seed_media_model_mapping
--
-- Advertise newly probed VolcEngine Ark media models on account 7, so the
-- volcengine/newapi compat pool can serve them through the OpenAI-compatible
-- media endpoints:
--   - account 7 "volcengine" (VolcEngine Ark, channel_type=45):
--       doubao-seedance-1-0-pro-fast-251015
--       doubao-seedream-4-5-251128
--       doubao-seedream-5-0-260128
--
-- Why (prod 2026-07-02 media expansion sweep): direct Ark data-plane probe
-- POST /api/v3/contents/generations/tasks returned HTTP 200 for
-- doubao-seedance-1-0-pro-fast-251015. The direct image probe initially used
-- 1024x1024 and got a request-shape 400 from Seedream 4.5/5.0; after fixing the
-- probe to 2048x2048, POST /api/v3/images/generations returned HTTP 200 for
-- doubao-seedream-4-5-251128 and doubao-seedream-5-0-260128. The existing account
-- model_mapping only advertised the four Seedance ids from tk_033 and Seedream
-- 4.0 from tk_037. A non-empty credentials.model_mapping is a strict allowlist
-- (service.Account.IsModelSupported), so without these identity keys the
-- scheduler would empty the single-account pool even though the upstream account
-- can serve the models.
--
-- Pricing ships in backend/internal/service/tk_pricing_overlay.json in the same
-- change (Seedance output_cost_per_second from the official 4.2 CNY/M-token
-- online rate; Seedream output_cost_per_image from official 0.25/0.22 CNY/image
-- rates; CNY/USD=6.7), so the whitelist add lands on already-priced names —
-- no $0 / unpriced-400 window.
--
-- Merge (jsonb ||) preserves existing chat, seedream image, and seedance video
-- identity-whitelist entries. Raw-SQL account mutation bypasses Ent snapshot
-- refresh hooks, so enqueue scheduler_outbox account_changed to hot-reload the
-- running scheduler.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH upd_volcengine AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "doubao-seedance-1-0-pro-fast-251015": "doubao-seedance-1-0-pro-fast-251015",
                "doubao-seedream-4-5-251128": "doubao-seedream-4-5-251128",
                "doubao-seedream-5-0-260128": "doubao-seedream-5-0-260128"
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
