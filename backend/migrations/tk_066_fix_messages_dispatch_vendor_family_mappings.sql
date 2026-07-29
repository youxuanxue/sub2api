-- Migration: tk_066_fix_messages_dispatch_vendor_family_mappings
--
-- Correct Claude-Code /v1/messages dispatch mappings that were copy-pasted from
-- GPT defaults onto non-OpenAI vendor groups. Runtime + admin validation now
-- read tk_messages_dispatch_family_registry.json; this migration aligns prod
-- rows with that registry.
--
-- Idempotent: each UPDATE is guarded by (id, name, platform) or platform/name.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

UPDATE groups
SET messages_dispatch_model_config = '{
        "opus_mapped_model": "gemini-2.5-pro",
        "sonnet_mapped_model": "gemini-3.6-flash",
        "haiku_mapped_model": "gemini-3.5-flash-lite"
    }'::jsonb,
    updated_at = NOW()
WHERE id = 16
  AND name = 'Google-Vertex'
  AND platform = 'newapi';

UPDATE groups
SET messages_dispatch_model_config = '{
        "opus_mapped_model": "glm-5.2",
        "sonnet_mapped_model": "glm-4.7",
        "haiku_mapped_model": "glm-4.5-air"
    }'::jsonb,
    updated_at = NOW()
WHERE id = 284
  AND name = 'glm'
  AND platform = 'newapi';

UPDATE groups
SET messages_dispatch_model_config = '{
        "opus_mapped_model": "kimi-k3",
        "sonnet_mapped_model": "kimi-k2.6",
        "haiku_mapped_model": "kimi-k2.5"
    }'::jsonb,
    updated_at = NOW()
WHERE id = 285
  AND name = 'Kimi'
  AND platform = 'newapi';

UPDATE groups
SET messages_dispatch_model_config = '{
        "opus_mapped_model": "qwen3.7-max",
        "sonnet_mapped_model": "qwen3.7-plus",
        "haiku_mapped_model": "qwen3.6-flash"
    }'::jsonb,
    updated_at = NOW()
WHERE id = 18
  AND name = 'Qwen'
  AND platform = 'newapi';

UPDATE groups
SET messages_dispatch_model_config = '{
        "opus_mapped_model": "doubao-seed-2-0-pro-260215",
        "sonnet_mapped_model": "doubao-seed-2-0-code-preview-260215",
        "haiku_mapped_model": "doubao-seed-2-0-mini-260428"
    }'::jsonb,
    updated_at = NOW()
WHERE id = 5
  AND name = 'volcengine'
  AND platform = 'newapi';

UPDATE groups
SET messages_dispatch_model_config = '{
        "opus_mapped_model": "gpt-5.6-sol",
        "sonnet_mapped_model": "gpt-5.6-terra",
        "haiku_mapped_model": "gpt-5.6-luna"
    }'::jsonb,
    updated_at = NOW()
WHERE id = 22
  AND name = '邀请试用'
  AND platform = 'openai';

UPDATE groups
SET messages_dispatch_model_config = '{
        "opus_mapped_model": "deepseek-v4-pro",
        "sonnet_mapped_model": "deepseek-v4-flash",
        "haiku_mapped_model": "deepseek-v4-flash"
    }'::jsonb,
    updated_at = NOW()
WHERE id = 11
  AND name = 'deepseek'
  AND platform = 'newapi';

UPDATE groups
SET messages_dispatch_model_config = '{
        "opus_mapped_model": "glm-5.2",
        "sonnet_mapped_model": "glm-4.7",
        "haiku_mapped_model": "glm-4.5-air"
    }'::jsonb,
    updated_at = NOW()
WHERE name = 'china'
  AND platform = 'newapi';

UPDATE groups
SET messages_dispatch_model_config = jsonb_set(
        messages_dispatch_model_config,
        '{opus_mapped_model}',
        '"grok-4.5"'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE platform = 'grok'
  AND allow_messages_dispatch = true
  AND COALESCE(messages_dispatch_model_config->>'opus_mapped_model', '') IN ('grok-4.3', '');

UPDATE groups
SET messages_dispatch_model_config = jsonb_set(
        messages_dispatch_model_config,
        '{sonnet_mapped_model}',
        '"grok-4.3"'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE platform = 'grok'
  AND allow_messages_dispatch = true
  AND COALESCE(messages_dispatch_model_config->>'sonnet_mapped_model', '') = 'grok-code-fast-1';
