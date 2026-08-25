-- tk_089_seed_optout_killswitch_settings.sql
--
-- Seed the four opt-out gateway kill-switches that were introduced with an
-- accessor but never with a row, so they exist in `settings` and are therefore
-- visible and editable in the admin UI.
--
-- Background. Each of these keys ships a default-ON accessor
-- (Is*Enabled → true unless the stored value is the literal "false"):
--
--   gateway.anthropic_saturated_stub_deprioritize.enabled   PR #743  (2026-06-12)
--   gateway.openai_saturated_stub_deprioritize.enabled      PR #1246 (2026-07-06)
--   gateway.sticky_routing.enabled                          present on prod, absent on edges
--   gateway.sticky_slot_full_escape.enabled                  never seeded
--
-- #743 documented its key as an operator kill-switch ("默认 ON，operator 可关"),
-- but with no row in `settings` the admin UI never lists it, so the switch could
-- only be exercised by writing SQL by hand. Seeding the effective default makes
-- the promised control real.
--
-- Why a migration and not InitializeDefaultSettings: that function returns early
-- when the settings table is already populated (it probes
-- SettingKeyRegistrationEnabled first), so its defaults map only ever reaches a
-- brand-new database. Every existing deployment — prod and all edges — would
-- keep missing these keys. See backend/internal/service/setting_parse.go.
--
-- Value written is "true", which is exactly the behavior these deployments
-- already had via the fail-open default. This migration is therefore a no-op
-- behavior change; it only makes the state explicit.
--
-- Idempotent, and deliberately non-destructive: ON CONFLICT DO NOTHING means an
-- operator who has already set any of these to "false" keeps that value. Re-runs
-- match zero rows.
--
-- Note: the accompanying code change (tkResolveOptOutFlagValue) makes an absent
-- key silent on its own, so this migration is not required to stop the log
-- noise. It is here for admin-UI visibility of the kill-switches.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

INSERT INTO settings (key, value, updated_at)
VALUES
    ('gateway.anthropic_saturated_stub_deprioritize.enabled', 'true', NOW()),
    ('gateway.openai_saturated_stub_deprioritize.enabled',    'true', NOW()),
    ('gateway.sticky_routing.enabled',                        'true', NOW()),
    ('gateway.sticky_slot_full_escape.enabled',               'true', NOW())
ON CONFLICT (key) DO NOTHING;
