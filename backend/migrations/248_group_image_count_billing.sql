-- bluegreen-safe-destructive-ok: upstream ADD COLUMN NOT NULL absorbed; constant DEFAULT false is metadata-only on PG16 (no table rewrite), old binaries ignore the column during blue/green.
-- 分组级按张计费开关：仅 OpenAI 与 Composite 分组使用。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS image_count_billing BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN groups.image_count_billing IS '图片生成是否按实际出图张数计费';
