-- TokenKey: drop GPT-series ids from CloudWise relay account model_mapping floors.
--
-- CloudWise MaaS relays expose Claude/Gemini/DeepSeek/GLM/etc. via dual-stack
-- chat + native messages, but GPT ids are not a supported routing surface for
-- these accounts (count_tokens/input_tokens gaps caused prod #95 auth_error).
-- Runtime SSOT (supportedOpenAICloudwiseRelayCatalogModels) no longer lists
-- gpt-*; this migration prunes any persisted gpt-* whitelist keys.
--
-- Idempotent: re-running is a no-op when no gpt-* keys remain.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH cloudwise AS (
    SELECT
        a.id,
        a.credentials,
        COALESCE(
            (
                SELECT jsonb_object_agg(e.key, e.value)
                FROM jsonb_each(a.credentials -> 'model_mapping') AS e(key, value)
                WHERE e.key NOT LIKE 'gpt-%'
            ),
            '{}'::jsonb
        ) AS new_mapping
    FROM accounts AS a
    WHERE a.deleted_at IS NULL
      AND a.platform = 'openai'
      AND a.type = 'apikey'
      AND LOWER(TRIM(BOTH '/' FROM a.credentials->>'base_url')) IN (
          'https://api.cloudwise.ai/api',
          'https://api-us.cloudwise.ai/api'
      )
      AND jsonb_typeof(a.credentials -> 'model_mapping') = 'object'
      AND EXISTS (
          SELECT 1
          FROM jsonb_object_keys(a.credentials -> 'model_mapping') AS k(key)
          WHERE k.key LIKE 'gpt-%'
      )
),
upd AS (
    UPDATE accounts AS a
    SET credentials = jsonb_set(a.credentials, '{model_mapping}', c.new_mapping, true),
        updated_at = NOW()
    FROM cloudwise AS c
    WHERE a.id = c.id
      AND a.credentials -> 'model_mapping' IS DISTINCT FROM c.new_mapping
    RETURNING a.id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
