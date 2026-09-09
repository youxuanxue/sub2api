-- bluegreen-safe-destructive-ok: expand-only defaulted columns; old app writers omit peak-rate fields and old readers ignore them.
ALTER TABLE groups ADD COLUMN IF NOT EXISTS peak_rate_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE groups ADD COLUMN IF NOT EXISTS peak_start VARCHAR(5) NOT NULL DEFAULT '';
ALTER TABLE groups ADD COLUMN IF NOT EXISTS peak_end VARCHAR(5) NOT NULL DEFAULT '';
ALTER TABLE groups ADD COLUMN IF NOT EXISTS peak_rate_multiplier DECIMAL(10,4) NOT NULL DEFAULT 1.0;
