-- TokenKey: CloudWise relay model_mapping → add hy3* wildcard floor entry.
--
-- Runtime SSOT: backend/internal/service/openai_cloudwise_relay_tk.go
-- (openAICloudwiseRelayAllowedModelPrefixes). Supplier account #114 serves hy3;
-- legacy wildcard-floor CloudWise accounts need the matching mapping key.
--
-- Idempotent: re-running is a no-op when mapping already matches the floor.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH want AS (
    SELECT jsonb_build_object(
        'kimi-*', 'kimi-*',
        'claude-*', 'claude-*',
        'glm-*', 'glm-*',
        'minimax-*', 'minimax-*',
        'deepseek-*', 'deepseek-*',
        'hy3*', 'hy3*'
    ) AS mapping
),
cloudwise AS (
    SELECT a.id, w.mapping AS new_mapping
    FROM accounts AS a
    CROSS JOIN want AS w
    WHERE a.deleted_at IS NULL
      AND a.platform = 'openai'
      AND a.type = 'apikey'
      AND LOWER(TRIM(BOTH '/' FROM a.credentials->>'base_url')) IN (
          'https://api.cloudwise.ai/api',
          'https://api-us.cloudwise.ai/api'
      )
      AND COALESCE(a.credentials -> 'model_mapping', '{}'::jsonb) IS DISTINCT FROM w.mapping
),
upd AS (
    UPDATE accounts AS a
    SET credentials = jsonb_set(a.credentials, '{model_mapping}', c.new_mapping, true),
        updated_at = NOW()
    FROM cloudwise AS c
    WHERE a.id = c.id
    RETURNING a.id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
