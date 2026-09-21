-- Migration: tk_100_antigravity_ide_cloudcode_profile
-- Seed the official Antigravity IDE language-server cloudcode ClientHello.
-- The empty ALPN array is intentional: the explicit extension list omits 16.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

INSERT INTO tls_fingerprint_profiles (
    name, description, enable_grease, shuffle_extensions,
    cipher_suites, curves, point_formats, signature_algorithms,
    alpn_protocols, supported_versions, key_share_groups, psk_modes, extensions
)
VALUES (
    'tk_canonical_antigravity_ide_cloudcode',
    'Official Antigravity IDE language-server cloudcode ClientHello captured locally from cloudcode-pa.googleapis.com; no ALPN extension; paired with antigravity/hub/<ver> darwin/arm64.',
    false,
    false,
    '[49195,49199,49196,49200,52393,52392,49161,49171,49162,49172,4865,4866,4867]'::jsonb,
    '[4588,4587,4589,29,23,24,25]'::jsonb,
    '[0]'::jsonb,
    '[2308,2309,2310,2052,1027,2055,2053,2054,1025,1281,1537,1283,1539]'::jsonb,
    '[]'::jsonb,
    '[772,771]'::jsonb,
    '[4588,29]'::jsonb,
    '[]'::jsonb,
    '[0,11,65281,23,18,5,10,13,50,43,51]'::jsonb
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
