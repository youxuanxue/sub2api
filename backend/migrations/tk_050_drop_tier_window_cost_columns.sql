-- Anthropic window scheduling now uses upstream passive utilization (global 0.98/0.02),
-- not per-tier dollar caps stored on tiers.
ALTER TABLE tiers DROP COLUMN IF EXISTS window_cost_limit;
ALTER TABLE tiers DROP COLUMN IF EXISTS window_cost_sticky_reserve;
