-- Migration: tk_066_fix_messages_dispatch_vendor_family_mappings
--
-- Correct Claude-Code /v1/messages dispatch mappings that were copy-pasted from
-- GPT defaults onto non-OpenAI vendor groups. Runtime + admin validation now
-- read tk_messages_dispatch_family_registry.json; this migration aligns prod
-- rows with that registry.
--
-- Idempotent: each UPDATE is guarded by (id, name, platform).

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

UPDATE groups
SET messages_dispatch_model_config = '{
        "opus_mapped_model": "gemini-2.5-pro",
        "sonnet_mapped_model": "gemini-2.5-flash",
        "haiku_mapped_model": "gemini-2.5-flash-lite"
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
        "opus_mapped_model": "kimi-k2.6",
        "sonnet_mapped_model": "kimi-k2.5",
        "haiku_mapped_model": "kimi-k2.5"
    }'::jsonb,
    updated_at = NOW()
WHERE id = 285
  AND name = 'Kimi'
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
SET messages_dispatch_model_config = jsonb_set(
        messages_dispatch_model_config,
        '{haiku_mapped_model}',
        '"doubao-seed-1-6-flash-250615"'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE id = 5
  AND name = 'volcengine'
  AND platform = 'newapi'
  AND COALESCE(messages_dispatch_model_config->>'haiku_mapped_model', '') = 'glm-4-7-251222';
