-- bluegreen-safe-destructive-ok: expand-only columns with constant defaults; old app ignores them; keep columns on rollback.
-- 2026-11-18 read-only evidence: prod/cn-prod groups=114/12, channel pricing=0/0, rollup state=1/1 (PG 18.6).
SET LOCAL lock_timeout = '3s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS force_openai_ultrafast BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.force_openai_ultrafast IS
    'Force service_tier=ultrafast on OpenAI gateway requests in this group for models that support Ultrafast, before global Fast/Flex policy evaluation; takes precedence over force_openai_fast';
