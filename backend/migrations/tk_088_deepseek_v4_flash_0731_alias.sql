-- Migration: tk_088_deepseek_v4_flash_0731_alias
--
-- Serve client id "deepseek-v4-flash-0731" from the Ark Agent Plan account by
-- REWRITING it onto the upstream id that account actually serves:
--
--     account 88 "volcengine-agent-plan" (channel_type=45, Ark /api/plan/v3)
--
-- Account 39 "ds-官" (channel_type=43, api.deepseek.com) is deliberately EXCLUDED.
-- It advertises V4-Flash and would be the natural second home for this alias, but
-- every DeepSeek request routed to it currently fails closed: 2026-08-25 00:40-01:20
-- UTC it served 0 successes against 219 upstream 502s (upstream_status_code=null,
-- "Upstream request failed"), the #1806 newapi CC-bridge bug. While it stayed in
-- this alias, scheduler fan-out sent ~40% of user38's -0731 traffic to it and every
-- one of those became a client-visible 502; the surviving ~60% on account 88 all
-- succeeded. Adding an account here only helps if that account can serve the model,
-- so 39 stays out until #1806 ships. The live key was removed out-of-band via
-- ops/newapi/apply-model-mapping-live.py remove-live; this file is the source of
-- truth that must not re-add it.
--
-- Why a rewrite and not an identity key: "-0731" is a Baidu-Qianfan-only dated SKU
-- name. Probed 2026-08-24 via the admin fetch-upstream-models endpoint, DeepSeek
-- official (ch43) advertises exactly three chat ids — deepseek-v4-flash,
-- deepseek-v4-flash-vision-exp, deepseek-v4-pro — and none carries the -0731
-- suffix. An identity mapping would forward an id the upstream rejects (404
-- model-not-found surfaced as 502). The upstream truth is that -0731 IS
-- V4-Flash: DeepSeek's own price page names the model "DeepSeek-V4-Flash-0731".
--
-- Billing: usage bills by the CLIENT-facing id, so these rows settle on the
-- registry owner "deepseek-v4-flash" (official list price + the official
-- 09:00-12:00 / 14:00-18:00 Asia/Shanghai peak window), same as any other
-- V4-Flash request. The Qianfan-scoped price key this id used to carry was
-- removed in the same change — see tk_pricing_overlay.json.
--
-- Account 90 (Qianfan, ch46) keeps its own identity mapping: Qianfan does serve
-- the dated id verbatim. It is intentionally NOT touched here.
--
-- model_mapping is normally an identity whitelist; a rewriting entry is the
-- documented exception for a vendor-specific SKU alias (same shape as the
-- CloudWise prefix floor in tk_082).
--
-- Idempotent: jsonb || re-applies the same single key and leaves every other
-- mapping entry untouched. Guarded on id + name + platform + channel_type + deleted_at
-- so a renumbered account cannot be hit by accident.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH upd AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb)
                || '{"deepseek-v4-flash-0731": "deepseek-v4-flash"}'::jsonb
        ),
        updated_at = NOW()
    WHERE platform = 'newapi'
      AND deleted_at IS NULL
      AND id = 88
      AND name = 'volcengine-agent-plan'
      AND channel_type = 45
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
