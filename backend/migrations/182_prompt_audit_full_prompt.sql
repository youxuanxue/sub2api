-- bluegreen-safe-destructive-ok: expand-only ADD COLUMN with NOT NULL DEFAULT ''; old app ignores new column.
-- Retain the full (unredacted) prompt text on audit events so admins can
-- review the exact content that triggered a finding. Scoped to events only:
-- transient processing jobs keep storing redacted metadata.
ALTER TABLE prompt_audit_events
    ADD COLUMN IF NOT EXISTS full_prompt TEXT NOT NULL DEFAULT '';
