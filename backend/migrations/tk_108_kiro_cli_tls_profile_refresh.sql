-- Migration: tk_108_kiro_cli_tls_profile_refresh
--
-- Project the kiro-cli 3.0.0 rustls ClientHello onto the existing
-- tk_canonical_kiro_cli row. Captured 2026-11-04 from three logged-in
-- `kiro-cli chat --v2` connections to runtime.us-east-1.kiro.dev.
-- Runtime digest f8e00beb1fafe8e50057236d677a8a5ad811d62de104fbaa429a84620b5ddcf0.
-- Immutable migration tk_081 remains history. Safe to re-run.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

INSERT INTO tls_fingerprint_profiles (
    name, description, enable_grease, shuffle_extensions,
    cipher_suites, curves, point_formats, signature_algorithms,
    alpn_protocols, supported_versions, key_share_groups, psk_modes, extensions
)
VALUES (
    'tk_canonical_kiro_cli',
    'TokenKey canonical Kiro CLI TLS profile captured from real kiro-cli 3.0.0 traffic; rustls permutes the observed extension set for each ClientHello and offers X25519MLKEM768 (group 4588).',
    false,
    true,
    '[4866,4865,4867,49196,49195,52393,49200,49199,52392,255]'::jsonb,
    '[4588,29,23,24]'::jsonb,
    '[0]'::jsonb,
    '[1283,1027,1539,2055,2054,2053,2052,1537,1281,1025,2308,2309,2310]'::jsonb,
    '[]'::jsonb,
    '[772,771]'::jsonb,
    '[4588,29]'::jsonb,
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
