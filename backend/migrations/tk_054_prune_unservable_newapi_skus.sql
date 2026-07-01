-- TokenKey: align account model_mapping with tk_served_models.json SSOT.
--
-- glm-4-32b-0414-128k was removed from the served-models manifest after prod
-- livefire returned upstream 400 model_not_found on account 67 (ZhipuV4).
-- Keeping the id in credentials.model_mapping advertised a model the gateway
-- could route but not serve — the same class of drift catalog-serving-drift.py
-- guards against (#812).
--
-- Idempotent: re-running is a no-op when the key is already absent.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH upd AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) - 'glm-4-32b-0414-128k',
            true
        ),
        updated_at = NOW()
    WHERE id = 67
      AND platform = 'newapi'
      AND deleted_at IS NULL
      AND credentials -> 'model_mapping' ? 'glm-4-32b-0414-128k'
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
