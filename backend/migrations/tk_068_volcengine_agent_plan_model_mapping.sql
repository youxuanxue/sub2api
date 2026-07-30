-- Migration: tk_068_volcengine_agent_plan_model_mapping
--
-- Wire VolcEngine Agent Plan account 88 (platform=newapi, channel_type=45)
-- with the empirically-servable Agent Plan dot-notation model set probed on
-- 2026-07-30 via ops/pricing/probe-volcengine-agent-plan-models.sh.
--
-- Agent Plan uses /api/plan/v3/* (not pay-as-you-go /api/v3/*). model_mapping
-- is an identity whitelist (key===value). base_url stores the direct
-- OpenAI-compatible Agent Plan root so TokenKey does not require a patched
-- new-api adaptor.
--
-- Idempotent: re-running overwrites model_mapping/base_url with the same values
-- and enqueues one scheduler_outbox refresh.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH upd AS (
    UPDATE accounts
    SET credentials = jsonb_set(
            jsonb_set(
                credentials,
                '{base_url}',
                '"https://ark.cn-beijing.volces.com/api/plan/v3"'::jsonb
            ),
            '{model_mapping}',
            '{
                "ark-code-latest": "ark-code-latest",
                "doubao-seed-2.0-mini": "doubao-seed-2.0-mini",
                "doubao-seed-2.0-lite": "doubao-seed-2.0-lite",
                "doubao-seed-evolving": "doubao-seed-evolving",
                "doubao-seed-2.0-code": "doubao-seed-2.0-code",
                "doubao-seed-2.0-pro": "doubao-seed-2.0-pro",
                "deepseek-v4-flash": "deepseek-v4-flash",
                "deepseek-v4-pro": "deepseek-v4-pro",
                "glm-5.2": "glm-5.2",
                "kimi-k2.6": "kimi-k2.6",
                "kimi-k2.7-code": "kimi-k2.7-code",
                "kimi-k3": "kimi-k3",
                "minimax-m2.7": "minimax-m2.7",
                "minimax-m3": "minimax-m3"
            }'::jsonb
        ),
        schedulable = true,
        updated_at = NOW()
    WHERE id = 88
      AND platform = 'newapi'
      AND channel_type = 45
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
