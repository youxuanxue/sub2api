-- bluegreen-safe-destructive-ok: expand-only ADD COLUMN with NOT NULL DEFAULT false; old app ignores new column.
ALTER TABLE groups ADD COLUMN allow_live BOOLEAN NOT NULL DEFAULT false;
