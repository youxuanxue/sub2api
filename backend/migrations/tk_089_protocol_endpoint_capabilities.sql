-- Migration: tk_089_protocol_endpoint_capabilities
-- Endpoint-scoped protocol capability SSOT. This migration is additive; the
-- application performs identity backfill, positive historical union, probing,
-- rollback projection, and readiness evaluation before serving traffic.

CREATE TABLE IF NOT EXISTS protocol_endpoint_capabilities (
    id BIGSERIAL PRIMARY KEY,
    capability_key VARCHAR(64) NOT NULL UNIQUE,
    identity JSONB NOT NULL,
    supported_protocols JSONB NOT NULL DEFAULT '[]'::jsonb,
    probe_evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    last_probed_at TIMESTAMPTZ NULL,
    probe_lease_owner TEXT NULL,
    probe_lease_until TIMESTAMPTZ NULL,
    probe_generation BIGINT NOT NULL DEFAULT 0 CHECK (probe_generation >= 0),
    identity_conflict BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT protocol_endpoint_capabilities_supported_protocols_array
        CHECK (jsonb_typeof(supported_protocols) = 'array'),
    CONSTRAINT protocol_endpoint_capabilities_probe_evidence_object
        CHECK (jsonb_typeof(probe_evidence) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_protocol_endpoint_capabilities_probe_lease_until
    ON protocol_endpoint_capabilities (probe_lease_until);
CREATE INDEX IF NOT EXISTS idx_protocol_endpoint_capabilities_identity_conflict
    ON protocol_endpoint_capabilities (identity_conflict);

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS protocol_endpoint_capability_id BIGINT;

CREATE INDEX IF NOT EXISTS idx_accounts_protocol_endpoint_capability_id
    ON accounts (protocol_endpoint_capability_id);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'accounts_protocol_endpoint_capability_id_fkey'
    ) THEN
        ALTER TABLE accounts
            ADD CONSTRAINT accounts_protocol_endpoint_capability_id_fkey
            FOREIGN KEY (protocol_endpoint_capability_id)
            REFERENCES protocol_endpoint_capabilities(id)
            ON DELETE RESTRICT;
    END IF;
END $$;

CREATE OR REPLACE FUNCTION prevent_protocol_endpoint_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.capability_key IS DISTINCT FROM OLD.capability_key
       OR NEW.identity IS DISTINCT FROM OLD.identity THEN
        RAISE EXCEPTION 'protocol endpoint capability identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_protocol_endpoint_identity_immutable
    ON protocol_endpoint_capabilities;
CREATE TRIGGER trg_protocol_endpoint_identity_immutable
BEFORE UPDATE OF capability_key, identity ON protocol_endpoint_capabilities
FOR EACH ROW
EXECUTE FUNCTION prevent_protocol_endpoint_identity_mutation();
