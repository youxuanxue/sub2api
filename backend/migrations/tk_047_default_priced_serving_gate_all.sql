-- TokenKey: seed the priced-serving gate enabled-set to '*' (all platforms) on first launch.
--
-- docs/approved/priced-or-it-doesnt-ship.md (post-pivot: family floor + alert + converge)
-- enables the runtime gate for ALL platforms by default. Safe because floored families
-- (claude/gpt/gemini) never reject — they fall to a family-median floor (never $0, never 404) —
-- and only no-floor multi-vendor ids (newapi/国产 unknown) reject (the intended backstop).
--
-- The gate reads SettingService.IsPricedServingGateEnabled(platform), which returns false for a
-- MISSING `priced_serving_gate_enabled` row (fail-open-toward-off). The in-code cold-start default
-- (tkMergeDefaultColdStartSettings = "*") only runs inside InitializeDefaultSettings, which
-- early-returns on any non-empty DB AND is not invoked on a normal deploy at all — so on the
-- EXISTING prod database the gate would ship silently OFF without this migration.
--
-- This writes the launch default the same way tk_003 re-adopts the backend-mode default: an
-- idempotent seed that reaches existing prod boxes. "*" is the all-platforms wildcard
-- (IsPricedServingGateEnabled treats "*" as matching every platform).
--
-- Idempotent: ON CONFLICT (key) DO NOTHING so re-running never clobbers whatever value an operator
-- later sets in the admin console (a comma platform list to narrow scope, or "" to roll back).
-- Only installs that never had the row get the '*' default.
--
-- See CLAUDE.md Hard Rule §5.x ("override the default via migration") for the discipline this follows.

INSERT INTO settings (key, value)
VALUES ('priced_serving_gate_enabled', '*')
ON CONFLICT (key) DO NOTHING;
