-- Keep the DB constraint in sync with service.AllowedQuotaPlatforms after
-- upstream migration 224 replaced the earlier newapi/kiro allowance.
ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE user_platform_quotas
    ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok',
                        'kimi', 'zhipu', 'deepseek', 'newapi', 'kiro'));
