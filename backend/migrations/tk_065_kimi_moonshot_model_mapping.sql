-- Migration: tk_065_kimi_moonshot_model_mapping
--
-- Persist the empirically verified Moonshot China model surface on prod account
-- 83 (kimi, newapi channel_type=25). All twelve IDs were returned by the
-- authenticated upstream /v1/models endpoint and returned HTTP 200 from a
-- minimal direct chat request on 2026-07-20; kimi-k2.5 additionally passed the
-- isolated prod gateway account probe with usage_logs.account_id=83.
--
-- Merge (jsonb ||) preserves compatible existing whitelist entries. Raw-SQL
-- account mutation bypasses Ent snapshot hooks, so enqueue scheduler_outbox
-- account_changed in the same migration.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH upd AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "kimi-k2.5": "kimi-k2.5",
                "kimi-k2.6": "kimi-k2.6",
                "kimi-k2.7-code": "kimi-k2.7-code",
                "kimi-k2.7-code-highspeed": "kimi-k2.7-code-highspeed",
                "kimi-k3": "kimi-k3",
                "moonshot-v1-8k": "moonshot-v1-8k",
                "moonshot-v1-8k-vision-preview": "moonshot-v1-8k-vision-preview",
                "moonshot-v1-32k": "moonshot-v1-32k",
                "moonshot-v1-32k-vision-preview": "moonshot-v1-32k-vision-preview",
                "moonshot-v1-128k": "moonshot-v1-128k",
                "moonshot-v1-128k-vision-preview": "moonshot-v1-128k-vision-preview",
                "moonshot-v1-auto": "moonshot-v1-auto"
            }'::jsonb
        ),
        updated_at = NOW()
    WHERE id = 83
      AND name = 'kimi'
      AND platform = 'newapi'
      AND channel_type = 25
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
