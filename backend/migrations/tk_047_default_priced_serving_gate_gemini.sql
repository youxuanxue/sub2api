-- TokenKey: seed the priced-serving gate enabled-set to 'gemini' on first launch.
--
-- docs/approved/priced-or-it-doesnt-ship.md ships the runtime "priced-or-it-
-- doesnt-ship" gate ON for gemini/Vertex in one step (§5/§8-D2). The gate reads
-- SettingService.IsPricedServingGateEnabled(platform), which returns false for a
-- MISSING `priced_serving_gate_enabled` row (fail-open-toward-off — see
-- setting_service_tk_cold_start.go). The in-code cold-start default
-- (tkMergeDefaultColdStartSettings = "gemini") only runs inside
-- InitializeDefaultSettings, which early-returns on any non-empty DB AND is not
-- invoked on a normal deploy at all — so on the EXISTING prod database the gate
-- would ship silently OFF (a no-op), leaving the $0-serving hole open.
--
-- This migration writes the launch default the same way tk_003 re-adopts the
-- backend-mode default: an idempotent seed that reaches existing prod boxes.
-- "gemini" also covers Vertex (Vertex accounts carry Platform="gemini").
--
-- Idempotent: ON CONFLICT (key) DO NOTHING so re-running never clobbers whatever
-- value an operator later sets in the admin console (including "" to roll the
-- gate back). Only installs that never had the row get the 'gemini' default.
--
-- See CLAUDE.md Hard Rule §5.x ("override the default via migration") for the
-- discipline this follows.

INSERT INTO settings (key, value)
VALUES ('priced_serving_gate_enabled', 'gemini')
ON CONFLICT (key) DO NOTHING;
