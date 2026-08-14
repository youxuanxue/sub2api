-- Add newapi and kiro to user_platform_quotas.platform CHECK constraint.
--
-- Live 1.8.151 billing writes user x platform quota usage for these platforms
-- when a user has platform quota limits configured. Keep the DB constraint aligned
-- with service.AllowedQuotaPlatforms and Ent validation.
ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE user_platform_quotas
    ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'newapi', 'kiro', 'grok'));
