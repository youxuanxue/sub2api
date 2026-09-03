-- TokenKey supplier-source row identity SSOT:
-- (supplier_name, endpoint, credential_fingerprint, channel_type)
-- supplier_lane remains the operator-facing supplier lane label only and exits the unique key.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM model_supplier_sources
        GROUP BY supplier_name, endpoint, credential_fingerprint, channel_type
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION
            'tk_093: cannot rewrite model_supplier_sources_identity_unique; duplicate (supplier_name, endpoint, credential_fingerprint, channel_type) rows exist';
    END IF;
END $$;

ALTER TABLE model_supplier_sources
    DROP CONSTRAINT IF EXISTS model_supplier_sources_identity_unique;

ALTER TABLE model_supplier_sources
    ADD CONSTRAINT model_supplier_sources_identity_unique
        UNIQUE (supplier_name, endpoint, credential_fingerprint, channel_type);
