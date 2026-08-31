-- Per-supplier default concurrency for managed projection accounts.

ALTER TABLE model_supplier_sources
    ADD COLUMN IF NOT EXISTS account_concurrency INTEGER NOT NULL DEFAULT 1000;

ALTER TABLE model_supplier_sources
    DROP CONSTRAINT IF EXISTS model_supplier_sources_account_concurrency_check;

ALTER TABLE model_supplier_sources
    ADD CONSTRAINT model_supplier_sources_account_concurrency_check
        CHECK (account_concurrency > 0);
