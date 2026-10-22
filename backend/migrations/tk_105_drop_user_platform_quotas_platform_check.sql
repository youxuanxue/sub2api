-- Re-assert upstream 242 policy after historical TK migrations
-- (tk_083 / tk_087 / tk_096) had restored user_platform_quotas_platform_check.
-- Idempotent DROP for environments that already applied those ADD variants;
-- fresh installs that only ever saw the DROP-only revisions are no-ops here.

ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE composite_model_routes
    DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;
