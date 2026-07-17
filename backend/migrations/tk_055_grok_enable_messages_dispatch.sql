-- Migration: tk_055_grok_enable_messages_dispatch
--
-- Enable Claude-Code (/v1/messages) dispatch for grok platform groups.
--
-- Grok rides the OpenAI-compat gateway (ForwardAsAnthropic → /v1/responses) but
-- route-layer and allow_messages_dispatch=false left Claude-Code clients on grok
-- groups with 404/403. Chat completions and responses HTTP were opened in #1132;
-- this completes the Anthropic-shaped entry for the same prod→edge relay path.
--
-- Also seeds Claude-family default mappings so claude-opus/sonnet/haiku resolve
-- to grok-4.3 / grok-code-fast-1 instead of falling back to GPT defaults.
--
-- Raw-SQL group mutation bypasses Ent hooks that enqueue scheduler snapshot
-- refresh — mirror tk_023 and emit scheduler_outbox group_changed per row.
--
-- Idempotent: only touches platform=grok groups still missing dispatch enablement
-- or still carrying empty / GPT-shaped messages_dispatch_model_config.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH upd AS (
    UPDATE groups
    SET allow_messages_dispatch = true,
        messages_dispatch_model_config = '{
            "opus_mapped_model": "grok-4.3",
            "sonnet_mapped_model": "grok-code-fast-1",
            "haiku_mapped_model": "grok-code-fast-1"
        }'::jsonb,
        updated_at = NOW()
    WHERE platform = 'grok'
      AND (
          allow_messages_dispatch = false
          OR COALESCE(messages_dispatch_model_config, '{}'::jsonb) = '{}'::jsonb
          OR COALESCE(messages_dispatch_model_config->>'opus_mapped_model', '') LIKE 'gpt-%'
          OR COALESCE(messages_dispatch_model_config->>'sonnet_mapped_model', '') LIKE 'gpt-%'
          OR COALESCE(messages_dispatch_model_config->>'haiku_mapped_model', '') LIKE 'gpt-%'
      )
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'group_changed', NULL, id, NULL FROM upd;
