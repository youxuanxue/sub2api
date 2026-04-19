-- TokenKey: default backend_mode_enabled to 'true'.
--
-- Re-adopts the upstream Backend Mode toggle (sub2api commit 6826149a) so that
-- TokenKey ships with admin-distributed account semantics by default
-- (registration / OAuth signup / self-service password reset / self-service
-- payment all blocked unless an admin explicitly turns Backend Mode off).
--
-- Idempotent: ON CONFLICT (key) DO NOTHING so re-running the migration does
-- not clobber whatever value an operator has set in the admin console after
-- first boot. Only fresh installs (and prod boxes that never had this row
-- because they were provisioned before the deletion was reverted) get the
-- 'true' default.
--
-- See CLAUDE.md Hard Rule §5 for the upstream-isolation discipline that
-- motivates "keep upstream files; override defaults via migration" instead of
-- "delete the file and lose the feature".

INSERT INTO settings (key, value)
VALUES ('backend_mode_enabled', 'true')
ON CONFLICT (key) DO NOTHING;
