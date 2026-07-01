-- Anthropic window scheduling now uses upstream passive utilization (global 0.98/0.02),
-- not per-tier dollar caps stored on tiers.
-- bluegreen-safe-destructive-ok: contract migration — old app ignores dropped tier columns;
-- new app no longer reads/writes window_cost_limit/window_cost_sticky_reserve (Ent schema regen).
ALTER TABLE tiers DROP COLUMN IF EXISTS window_cost_limit;
ALTER TABLE tiers DROP COLUMN IF EXISTS window_cost_sticky_reserve;
