-- bluegreen-safe-destructive-ok: expand-only DEFAULT 0 column; old app writers omit frozen_balance and old readers ignore it.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS frozen_balance DECIMAL(20,8) NOT NULL DEFAULT 0;
