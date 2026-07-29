-- TokenKey: keep upstream panel API rate limit opt-in until an operator enables it.
--
-- Upstream treats a missing panel_rate_limit_settings row as enabled=true
-- (240 user RPM / 60 heavy RPM). Prod dashboards, usage polling scripts, and
-- incident-response tooling must not start returning 429 immediately after deploy.
--
-- Idempotent: ON CONFLICT DO NOTHING — never clobber an operator's saved policy.

INSERT INTO settings (key, value)
VALUES (
  'panel_rate_limit_settings',
  '{"enabled":false,"user_rpm":240,"heavy_rpm":60,"exempt_admin":true,"public_ip_rpm":300}'
)
ON CONFLICT (key) DO NOTHING;
