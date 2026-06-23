-- Migration: tk_045_disable_cancel_storm_config
--
-- TokenKey cancel-storm detection is retired: prod generated noisy detect_only
-- alerts for short-window client cancels, and the runtime observation hook has
-- been removed. Keep the historical seed migration intact, but force any
-- previously enabled settings row back to off so older binaries or rollback
-- windows do not continue emitting alerts.

UPDATE settings
SET value = '{"mode":"off","window_seconds":60,"min_sample_count":20,"cancel_rate_threshold":0.5,"min_cancel_count":0,"alert_cooldown_seconds":600,"opus_only":false}',
    updated_at = NOW()
WHERE key = 'cancel_storm_config';
