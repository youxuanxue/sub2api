-- TokenKey supplier-source management facts.
-- Runtime scheduling continues to consume only the existing accounts table.

CREATE TABLE IF NOT EXISTS model_supplier_sources (
    id BIGSERIAL PRIMARY KEY,
    supplier_name VARCHAR(120) NOT NULL,
    channel_name VARCHAR(120) NOT NULL,
    endpoint TEXT NOT NULL,
    encrypted_credential TEXT NOT NULL,
    credential_fingerprint VARCHAR(128) NOT NULL,
    base_priority INTEGER NOT NULL DEFAULT 100,
    models JSONB NOT NULL DEFAULT '[]'::jsonb,
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT model_supplier_sources_models_array_check
        CHECK (jsonb_typeof(models) = 'array'),
    CONSTRAINT model_supplier_sources_identity_unique
        UNIQUE (supplier_name, channel_name, endpoint, credential_fingerprint)
);
