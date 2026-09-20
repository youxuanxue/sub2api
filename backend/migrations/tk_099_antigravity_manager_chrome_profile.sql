-- Migration: tk_099_antigravity_manager_chrome_profile
--
-- Seed the opt-in Antigravity-Manager-compatible route. This is intentionally
-- not the default for existing accounts. The runtime recognizes the canonical
-- name and uses the vendored Chrome preset while the first real Manager
-- ClientHello capture is pending; a later capture can replace the empty arrays
-- through the normal profile update path.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

INSERT INTO tls_fingerprint_profiles (
    name, description, enable_grease, shuffle_extensions,
    cipher_suites, curves, point_formats, signature_algorithms,
    alpn_protocols, supported_versions, key_share_groups, psk_modes, extensions
)
VALUES (
    'tk_canonical_antigravity_manager_chrome123',
    'Experimental Antigravity-Manager route: Chrome123-style transport plus Manager UA/x-client headers. Runtime uses uTLS Chrome120 compatibility preset until a real rquest Chrome123 ClientHello is captured; not enabled by default.',
    false,
    false,
    '[]'::jsonb,
    '[]'::jsonb,
    '[]'::jsonb,
    '[]'::jsonb,
    '["h2","http/1.1"]'::jsonb,
    '[]'::jsonb,
    '[]'::jsonb,
    '[]'::jsonb,
    '[]'::jsonb
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
