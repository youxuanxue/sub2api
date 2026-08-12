-- Tokensea relay (account 92): upstream supports native /v1/messages and
-- /v1/chat/completions but returns 500 not-implemented on /v1/responses.
-- 2026-08-12 prod matrix probe evidence.

UPDATE accounts
SET extra = COALESCE(extra, '{}'::jsonb)
    || jsonb_build_object(
        'openai_responses_supported', false,
        'openai_native_messages_supported', true
    )
WHERE id = 92
  AND deleted_at IS NULL
  AND LOWER(TRIM(BOTH '/' FROM credentials->>'base_url')) = 'https://agent.tokensea.ai';
