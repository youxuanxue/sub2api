-- Migration: tk_021_qwen_enable_and_claude_code_dispatch
--
-- Wire up the Qwen account (id=60 "Qwen", platform=openai, base_url=
-- https://dashscope.aliyuncs.com/compatible-mode/v1; group id=18 "Qwen") so it can
-- actually serve, with correct Claude-Code dispatch + non-zero billing.
--
-- Audit (2026-06-10, read-only probe):
--   - account 60 advertises only the qwen3.7-max family (qwen3.7-max + preview + 3
--     dated snapshots), all 200/servable on DashScope. It is currently
--     schedulable=false (offline).
--   - group 18 messages_dispatch_model_config was COPY-PASTED from a GPT group:
--     opus→gpt-5.5 / sonnet→gpt-5.3-codex / haiku→gpt-5.4-mini. A Claude Code client
--     (/v1/messages) on the Qwen group would be dispatched to GPT models the Qwen
--     account does NOT serve → wrong route / failure.
--   - qwen3.7-max has NO pricing (no channel pricing, not in litellm, not in overlay)
--     → would bill $0 (revenue leak + pricing_missing #688) once it serves.
--
-- This migration fixes (2) and (3); pricing (1) ships in the SAME release via
-- backend/internal/service/tk_pricing_overlay.json (qwen3.7-max* at the DashScope
-- official list price ¥12/¥36 per M, CNY/USD=7.3).

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- (2) Fix the Claude-Code dispatch mapping: map all three Claude tiers to the only
--     model the Qwen account advertises (qwen3.7-max). claude_code_only stays false
--     (the group already allows non-Claude-Code OpenAI clients too).
UPDATE groups
SET messages_dispatch_model_config = '{
        "opus_mapped_model": "qwen3.7-max",
        "sonnet_mapped_model": "qwen3.7-max",
        "haiku_mapped_model": "qwen3.7-max"
    }'::jsonb,
    updated_at = NOW()
WHERE id = 18
  AND name = 'Qwen'
  AND platform = 'openai';

-- (3) Enable the Qwen account now that pricing + dispatch are in place. Raw-SQL
--     account mutation bypasses the Ent hooks that enqueue a scheduler snapshot
--     refresh, so enqueue a scheduler_outbox account_changed event (pattern:
--     tk_015 / tk_020) — otherwise running replicas keep the stale schedulable=false
--     until their next full snapshot reload.
WITH upd AS (
    UPDATE accounts
    SET schedulable = true,
        updated_at = NOW()
    WHERE id = 60
      AND name = 'Qwen'
      AND platform = 'openai'
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
