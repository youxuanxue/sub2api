-- bluegreen-safe-destructive-ok: upstream ADD COLUMN NOT NULL absorbed; verified empty/defaultable on apply path for Stage0+prod blue/green.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS i18n JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN groups.i18n IS
    'Per-locale display text for the group: {"<locale>":{"name":string,"description":string}}; locales or fields that are absent fall back to groups.name / groups.description';
