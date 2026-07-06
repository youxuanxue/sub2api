-- TokenKey: align volcengine account model_mapping with tk_served_models.json SSOT.
--
-- glm-4-7-251222 was removed from the served-models manifest: GLM chat models are
-- served preferentially via Qwen/DashScope accounts 60/72 (glm-4.7, …), not
-- VolcEngine Ark account 7. Keeping the VolcEngine-specific SKU id in
-- credentials.model_mapping advertised a duplicate path the gateway could route
-- but should not prefer — the same class of drift catalog-serving-drift.py
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
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) - 'glm-4-7-251222',
            true
        ),
        updated_at = NOW()
    WHERE id = 7
      AND platform = 'newapi'
      AND deleted_at IS NULL
      AND credentials -> 'model_mapping' ? 'glm-4-7-251222'
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
