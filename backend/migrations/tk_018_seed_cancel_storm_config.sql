-- TokenKey: seed the per-API-key cancel-storm detector config. Default OFF.
--
-- Background: a single external automation client borrowing a TokenKey API key
-- with short client timeouts produced a "cancel storm" (context-canceled flood)
-- on opus relay traffic — the exact "non-human harness" traffic shape that trips
-- Anthropic's abuse filter and got an irreplaceable subscription-OAuth account
-- org-disabled. TokenKey had no real-time signal: the incident was only
-- reconstructed post-mortem. This detector counts per-API-key cancel rate at the
-- gateway terminal-outcome chokepoint and fires a Feishu alert when a key crosses
-- the threshold, so operators see the storm as it happens and can act (Phase 1:
-- detect + alert only; auto-throttle is a separate later PR).
--
-- Config shape (parsed by internal/service/ops_cancel_storm_tk.go):
--   mode                  : "off" (default) | "detect_only". Only "detect_only" arms it.
--   window_seconds        : tumbling counting window per key.
--   min_sample_count      : minimum requests in a window before the rate is judged
--                           (a volume floor so low-traffic keys never trip).
--   cancel_rate_threshold : canceled/total ratio that counts as a storm (0..1).
--   min_cancel_count      : optional absolute canceled floor (0 = rely on rate*samples).
--   alert_cooldown_seconds: per-key alert de-dup window (anti-spam).
--   opus_only             : optional gate to only count opus-family models.
--
-- Default mode is "off" so this migration is behaviorally inert on every existing
-- fleet node until an operator flips it to "detect_only" via the settings row.
-- These are STARTING values to be calibrated against real traffic under
-- detect_only before any future enforcement is enabled.
--
-- Idempotent: ON CONFLICT (key) DO NOTHING so re-running and operator overrides
-- are preserved; only fresh installs without the row get the default.

INSERT INTO settings (key, value)
VALUES (
  'cancel_storm_config',
  '{"mode":"off","window_seconds":60,"min_sample_count":20,"cancel_rate_threshold":0.5,"min_cancel_count":0,"alert_cooldown_seconds":600,"opus_only":false}'
)
ON CONFLICT (key) DO NOTHING;
