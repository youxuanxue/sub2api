-- Migration: tk_023_qwen_enable_messages_dispatch
--
-- Enable Claude-Code (/v1/messages) dispatch for the Qwen group (id=18).
--
-- Gap (found during v1.7.91 post-deploy verification): tk_021 set group 18's
-- messages_dispatch_model_config to {opus,sonnet,haiku -> qwen3.7-max} but left
-- allow_messages_dispatch=false. The /v1/messages handler gates on that flag
-- (backend/internal/handler/openai_gateway_handler.go: "if apiKey.Group != nil &&
-- !apiKey.Group.AllowMessagesDispatch -> 403 'This group does not allow
-- /v1/messages dispatch'"), so a Claude-Code client on the Qwen group was rejected
-- 403 and the opus/sonnet/haiku -> qwen3.7-max mapping never ran — the dispatch
-- config was inert. The DeepSeek group (id=11) already has the flag true, which is
-- why its Claude-Code path worked. This flips Qwen to match, completing the
-- Claude-Code support tk_021 intended.
--
-- Raw-SQL group mutation bypasses the Ent hooks that enqueue a scheduler snapshot
-- refresh, so enqueue a scheduler_outbox group_changed event (same pattern as
-- tk_022) — otherwise running replicas keep the stale flag until a full reload.
--
-- Idempotent + cross-deployment safe: guarded by (id, name, platform='newapi',
-- allow_messages_dispatch=false) so re-running is a no-op once enabled, and a bare
-- id colliding with an unrelated group in another DB cannot match.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH upd AS (
    UPDATE groups
    SET allow_messages_dispatch = true,
        updated_at = NOW()
    WHERE id = 18
      AND name = 'Qwen'
      AND platform = 'newapi'
      AND allow_messages_dispatch = false
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'group_changed', NULL, id, NULL FROM upd;
