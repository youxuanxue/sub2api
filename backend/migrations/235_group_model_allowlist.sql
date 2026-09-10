-- TK: keep the listing-only column usable by the active blue/green instance.
-- Existing listing preferences must not become request authorization rules.
-- bluegreen-safe-destructive-ok: additive JSONB column with a disabled default; old readers/writers remain valid.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS model_allowlist JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN groups.model_allowlist IS
    'Group model allowlist: constrains both model listing responses and request admission';
