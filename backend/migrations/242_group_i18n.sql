-- Bound blue/green startup DDL waits; a busy old instance must not stall live traffic.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- bluegreen-safe-destructive-ok: ADD COLUMN NOT NULL DEFAULT 常量（PostgreSQL 11+）仅改 catalog，
-- 不重写表、不扫描存量行；groups 为控制面小表；镜像回滚保留新增列，无数据回填依赖。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS i18n JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN groups.i18n IS
    'Per-locale display text for the group: {"<locale>":{"name":string,"description":string}}; locales or fields that are absent fall back to groups.name / groups.description';
