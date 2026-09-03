-- Rename supplier-source lane label column for SSOT naming clarity:
-- channel_name (ambiguous with TokenKey channel_type) → supplier_lane.
--
-- bluegreen-safe-destructive-ok: admin-only model_supplier_sources surface; gateway
-- scheduling never reads this column. Atomic rename preserves row data. Idempotent
-- when supplier_lane already exists (re-apply / overlap). Old color may fail
-- supplier Admin CRUD only during the brief drain window after migration applies.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'model_supplier_sources'
          AND column_name = 'channel_name'
    ) AND NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'model_supplier_sources'
          AND column_name = 'supplier_lane'
    ) THEN
        ALTER TABLE model_supplier_sources
            RENAME COLUMN channel_name TO supplier_lane;
    END IF;
END $$;
