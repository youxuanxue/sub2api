-- 风控中心：记录关键词拦截命中的具体关键词
-- bluegreen-safe-destructive-ok: expand-only defaulted column; old app writers omit matched_keyword and old readers ignore it.

ALTER TABLE content_moderation_logs ADD COLUMN IF NOT EXISTS matched_keyword VARCHAR(255) NOT NULL DEFAULT '';
