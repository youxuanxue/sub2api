-- TokenKey: migrate the GLM direct account from the deprecated Zhipu v3
-- adaptor (channel_type=16) to ZhipuV4/OpenAI-compatible (channel_type=26)
-- and expose only paid, officially-priced GLM chat SKUs.
--
-- Keep base_url at the host root: the ZhipuV4 adaptor appends
-- /api/paas/v4/... itself. A stored /api/paas/v4 suffix would double the path.
WITH upd AS (
    UPDATE accounts
    SET channel_type = 26,
        credentials = jsonb_set(
            jsonb_set(
                credentials,
                '{base_url}',
                to_jsonb('https://open.bigmodel.cn'::text),
                true
            ),
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "glm-5.2": "glm-5.2",
                "glm-5.1": "glm-5.1",
                "glm-5": "glm-5",
                "glm-5-turbo": "glm-5-turbo",
                "glm-4.7": "glm-4.7",
                "glm-4.7-flashx": "glm-4.7-flashx",
                "glm-4.6": "glm-4.6",
                "glm-4.5": "glm-4.5",
                "glm-4.5-x": "glm-4.5-x",
                "glm-4.5-air": "glm-4.5-air",
                "glm-4.5-airx": "glm-4.5-airx"
            }'::jsonb,
            true
        ),
        updated_at = NOW()
    WHERE id = 67
      AND name = 'GLM'
      AND platform = 'newapi'
      AND channel_type = 16
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
