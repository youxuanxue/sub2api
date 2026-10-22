-- Historical TK migration: previously re-ADDed user_platform_quotas_platform_check
-- after upstream 224. Align with 242: platform membership is application-owned.
-- DROP-only revision; prod that already applied the ADD is repaired by tk_105.

ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;
