-- Historical TK migration: previously re-ADDed user_platform_quotas_platform_check
-- to include newapi/kiro. Upstream 242 drops platform CHECKs so membership is
-- validated by the application catalog (domain.IsConcretePlatform /
-- service.AllowedQuotaPlatforms). Keep this file as DROP-only so later
-- migrations never restore a drifting DB whitelist.
--
-- Environments that already applied the ADD variant are cleaned by
-- tk_105_drop_user_platform_quotas_platform_check.sql; checksum compatibility
-- accepts both the historical ADD content and this DROP-only revision.

ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;
