-- Per-group access control when an explicit OpenAI/Codex reasoning effort
-- bluegreen-safe-destructive-ok: additive column defaults downgrade, preserving existing behavior and old writers.
-- exceeds max_reasoning_effort. Existing groups keep the previous behaviour
-- (automatically downgrade to the ceiling).
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS max_reasoning_effort_over_limit VARCHAR(20) NOT NULL DEFAULT 'downgrade';
