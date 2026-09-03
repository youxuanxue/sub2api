-- Rename supplier-source lane label column for SSOT naming clarity:
-- channel_name (ambiguous with TokenKey channel_type) → supplier_lane.

ALTER TABLE model_supplier_sources
    RENAME COLUMN channel_name TO supplier_lane;
