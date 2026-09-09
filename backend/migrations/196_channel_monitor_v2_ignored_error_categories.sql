-- Categories excluded from error_rate / health scoring (still shown in error breakdown).
-- bluegreen-safe-destructive-ok: expand-only ADD COLUMN with NOT NULL DEFAULT on channel_monitor_v2_config; old app ignores new column.
ALTER TABLE channel_monitor_v2_config
    ADD COLUMN IF NOT EXISTS ignored_error_categories TEXT[] NOT NULL DEFAULT '{}';
