-- Bound blue/green startup DDL waits; a busy old instance must not stall live traffic.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- bluegreen-safe-destructive-ok: ADD COLUMN NOT NULL DEFAULT 常量（PostgreSQL 11+）仅改 catalog，
-- 不重写表、不扫描存量行；gemini_cached_contents 行为追加写入模型；镜像回滚保留新增列。
-- Gemini 显式缓存创建时的渠道计费口径（渠道 ID / 请求模型 / 计费模型来源），
-- 延长有效期按同一口径计费。
ALTER TABLE gemini_cached_contents
    ADD COLUMN IF NOT EXISTS channel_usage JSONB NOT NULL DEFAULT '{}'::jsonb;
