-- Display-only ISO 4217 currency label for subscription plan prices; empty
-- keeps existing plans rendering without any label.
-- bluegreen-safe-destructive-ok: expand-only DEFAULT '' column; old app writers omit it and old readers ignore it.
ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS currency VARCHAR(3) NOT NULL DEFAULT '';
