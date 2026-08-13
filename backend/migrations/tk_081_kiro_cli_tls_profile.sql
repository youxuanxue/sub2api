-- Migration: tk_081_kiro_cli_tls_profile
--
-- Make the real Kiro CLI rustls profile the sole current Kiro TLS identity.
-- Immutable migration tk_014 remains history; this forward migration upserts the
-- CLI row, moves only explicit references to the old canonical row, proves no
-- references remain, then removes the old runtime row. Safe to re-run.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

INSERT INTO tls_fingerprint_profiles (
    name, description, enable_grease, shuffle_extensions,
    cipher_suites, curves, point_formats, signature_algorithms,
    alpn_protocols, supported_versions, key_share_groups, psk_modes, extensions
)
VALUES (
    'tk_canonical_kiro_cli',
    'TokenKey canonical Kiro CLI TLS profile captured from real kiro-cli traffic; rustls permutes the observed extension set for each ClientHello.',
    false,
    true,
    '[4866,4865,4867,49196,49195,52393,49200,49199,52392,255]'::jsonb,
    '[29,23,24]'::jsonb,
    '[0]'::jsonb,
    '[1283,1027,2055,2054,2053,2052,1537,1281,1025]'::jsonb,
    '[]'::jsonb,
    '[772,771]'::jsonb,
    '[29]'::jsonb,
    '[1]'::jsonb,
    '[0,5,10,11,13,23,35,43,45,51]'::jsonb
)
ON CONFLICT (name) DO UPDATE SET
    description = EXCLUDED.description,
    enable_grease = EXCLUDED.enable_grease,
    shuffle_extensions = EXCLUDED.shuffle_extensions,
    cipher_suites = EXCLUDED.cipher_suites,
    curves = EXCLUDED.curves,
    point_formats = EXCLUDED.point_formats,
    signature_algorithms = EXCLUDED.signature_algorithms,
    alpn_protocols = EXCLUDED.alpn_protocols,
    supported_versions = EXCLUDED.supported_versions,
    key_share_groups = EXCLUDED.key_share_groups,
    psk_modes = EXCLUDED.psk_modes,
    extensions = EXCLUDED.extensions,
    updated_at = NOW();

WITH profile_ids AS (
    SELECT
        MAX(id) FILTER (WHERE name = 'tk_canonical_kiro_ide') AS old_id,
        MAX(id) FILTER (WHERE name = 'tk_canonical_kiro_cli') AS new_id
    FROM tls_fingerprint_profiles
), rebound AS (
    UPDATE accounts
    SET extra = jsonb_set(
            COALESCE(extra, '{}'::jsonb),
            '{tls_fingerprint_profile_id}',
            to_jsonb(profile_ids.new_id),
            true
        ),
        updated_at = NOW()
    FROM profile_ids
    WHERE profile_ids.old_id IS NOT NULL
      AND profile_ids.new_id IS NOT NULL
      AND extra->>'tls_fingerprint_profile_id' = profile_ids.old_id::text
    RETURNING accounts.id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM rebound;

DO $$
DECLARE
    old_profile_id BIGINT;
    remaining_references BIGINT;
BEGIN
    SELECT id INTO old_profile_id
    FROM tls_fingerprint_profiles
    WHERE name = 'tk_canonical_kiro_ide';

    IF old_profile_id IS NULL THEN
        RETURN;
    END IF;

    SELECT COUNT(*) INTO remaining_references
    FROM accounts
    WHERE extra->>'tls_fingerprint_profile_id' = old_profile_id::text;

    IF remaining_references <> 0 THEN
        RAISE EXCEPTION
            'refusing to delete tk_canonical_kiro_ide: % account references remain',
            remaining_references;
    END IF;

    DELETE FROM tls_fingerprint_profiles WHERE id = old_profile_id;
END
$$;
