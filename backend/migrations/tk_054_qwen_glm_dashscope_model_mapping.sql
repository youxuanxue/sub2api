-- Migration: tk_054_qwen_glm_dashscope_model_mapping
--
-- Advertise DashScope GLM chat models on both Qwen accounts (id=60 primary,
-- id=72 Qwen-2 mirror backup), so the Qwen group pool can serve them through
-- dashscope.aliyuncs.com (channel_type=17):
--   glm-5.2, glm-5.1, glm-5, glm-4.7, glm-4.6, glm-4.5, glm-4.5-air
--
-- Mirror invariant (docs/approved/served-model-reconcile-planner.md):
--   account 72 model_mapping == account 60 model_mapping
--
-- DashScope model ids match the Alibaba百炼 GLM section (华北2北京 mainland).
-- User-facing overlay pricing follows Zhipu official list (https://open.bigmodel.cn/pricing).
-- Account 67 still serves the ZhipuV4 direct SKUs via tk_044.
--
-- Merge (jsonb ||) preserves existing whitelist entries. Raw-SQL account mutation
-- bypasses Ent snapshot hooks — enqueue scheduler_outbox account_changed per account.
--
-- Idempotent + cross-deployment safe: guarded by (id, name, platform='newapi',
-- channel_type=17).

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- Qwen account 60: add DashScope GLM identity whitelist entries.
WITH upd_qwen AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "glm-5.2": "glm-5.2",
                "glm-5.1": "glm-5.1",
                "glm-5": "glm-5",
                "glm-4.7": "glm-4.7",
                "glm-4.6": "glm-4.6",
                "glm-4.5": "glm-4.5",
                "glm-4.5-air": "glm-4.5-air"
            }'::jsonb
        ),
        updated_at = NOW()
    WHERE id = 60
      AND name = 'Qwen'
      AND platform = 'newapi'
      AND channel_type = 17
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd_qwen;

-- Qwen-2 account 72: mirror the same DashScope GLM whitelist (backup pool).
WITH upd_qwen2 AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            credentials,
            '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "glm-5.2": "glm-5.2",
                "glm-5.1": "glm-5.1",
                "glm-5": "glm-5",
                "glm-4.7": "glm-4.7",
                "glm-4.6": "glm-4.6",
                "glm-4.5": "glm-4.5",
                "glm-4.5-air": "glm-4.5-air"
            }'::jsonb
        ),
        updated_at = NOW()
    WHERE id = 72
      AND name = 'Qwen-2'
      AND platform = 'newapi'
      AND channel_type = 17
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd_qwen2;
