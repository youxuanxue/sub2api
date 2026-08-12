-- Tokensea Anthropic relay (account 93): align model_mapping with upstream wire
-- IDs, enable passthrough, and drop models absent from agent.tokensea.ai.
-- 2026-08-12 prod matrix probe evidence.

UPDATE accounts
SET credentials = jsonb_set(
        COALESCE(credentials, '{}'::jsonb),
        '{model_mapping}',
        '{
            "claude-fable-5": "claude-fable-5",
            "claude-haiku-4-5": "claude-haiku-4-5-20251001",
            "claude-opus-4-5": "claude-opus-4-5-20251101",
            "claude-opus-4-6": "claude-opus-4-6",
            "claude-opus-4-7": "claude-opus-4-7",
            "claude-opus-4-8": "claude-opus-4-8",
            "claude-opus-5": "claude-opus-5",
            "claude-sonnet-4-6": "claude-sonnet-4-6",
            "claude-sonnet-5": "claude-sonnet-5"
        }'::jsonb,
        true
    ),
    extra = COALESCE(extra, '{}'::jsonb)
        || jsonb_build_object('anthropic_passthrough', true)
WHERE id = 93
  AND deleted_at IS NULL
  AND LOWER(TRIM(BOTH '/' FROM credentials->>'base_url')) = 'https://agent.tokensea.ai';
