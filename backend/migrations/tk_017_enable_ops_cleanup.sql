-- TokenKey: re-enable ops data-retention cleanup that an upstream bug silently
-- disabled.
--
-- Background (root cause, confirmed on prod 2026-06-06):
--   Upstream's defaultOpsAdvancedSettings() seeds data_retention.cleanup_enabled
--   = false, and GetOpsAdvancedSettings() auto-persists that default to the
--   `settings` row on the FIRST read of the Ops settings page. computeEffective
--   then lets the settings value override config (which defaults enabled=true),
--   so the OpsCleanupService cron stops being scheduled the moment anyone opens
--   the Ops settings page. On prod this left ops_cleanup un-run for a month
--   (last_run 2026-05-06) while ops_system_logs (2.5GB) + ops_error_logs (1.7GB)
--   grew unbounded — 4.3GB of the 6.3GB database.
--
-- Why a migration (not a Go change): the buggy default + override live in
-- upstream-owned files (byte-identical to Wei-Shaw/sub2api). Per CLAUDE.md §5,
-- TokenKey overrides upstream defaults via migration rather than editing the
-- upstream function (same pattern as tk_003_default_backend_mode_enabled.sql).
-- The upstream bug itself should be fixed at the source via a separate PR; this
-- migration un-breaks the existing fleet (prod + every edge) on next deploy and
-- the deploy restart reschedules the cron from the now-true setting.
--
-- Scope: surgical flip of cleanup_enabled false/missing -> true on the EXISTING
-- settings row. It deliberately does NOT seed a fresh row and does NOT touch any
-- other field (a partial seed would zero the retention ints, which the cleaner
-- treats as "truncate all"). Fresh installs without a row are already enabled by
-- config default until the upstream bug is fixed.
--
-- Idempotent: the WHERE clause only matches rows where cleanup_enabled is
-- currently false or absent, so re-running is a no-op. Verified read-only
-- against the live prod row before authoring (before=false -> after=true,
-- error_log_retention_days and sibling fields preserved).

UPDATE settings
SET value = jsonb_set(value::jsonb, '{data_retention,cleanup_enabled}', 'true'::jsonb, true)::text,
    updated_at = NOW()
WHERE key = 'ops_advanced_settings'
  AND value IS NOT NULL
  AND jsonb_typeof(value::jsonb) = 'object'
  AND jsonb_typeof(value::jsonb -> 'data_retention') = 'object'
  AND COALESCE((value::jsonb #>> '{data_retention,cleanup_enabled}')::boolean, false) = false;
