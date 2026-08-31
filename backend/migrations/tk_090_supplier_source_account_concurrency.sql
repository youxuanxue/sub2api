-- Per-supplier default concurrency for managed projection accounts.
-- bluegreen-safe-destructive-ok: expand-only ADD COLUMN IF NOT EXISTS with DEFAULT 1000
-- backfills existing rows; no table rewrite or contract shrink.

ALTER TABLE model_supplier_sources
    ADD COLUMN IF NOT EXISTS account_concurrency INTEGER NOT NULL DEFAULT 1000;

ALTER TABLE model_supplier_sources
    DROP CONSTRAINT IF EXISTS model_supplier_sources_account_concurrency_check;

ALTER TABLE model_supplier_sources
    ADD CONSTRAINT model_supplier_sources_account_concurrency_check
        CHECK (account_concurrency > 0);
