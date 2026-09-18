-- Migration: tk_097_antigravity_cli_tls_profile
--
-- Seed the real Antigravity CLI (agy) TLS ClientHello as
-- tk_canonical_antigravity_cli. Captured 2026-09-18 from local agy 1.2.2 via
-- mitmproxy tls_clienthello (5 identical samples). Safe to re-run.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

INSERT INTO tls_fingerprint_profiles (
    name, description, enable_grease, shuffle_extensions,
    cipher_suites, curves, point_formats, signature_algorithms,
    alpn_protocols, supported_versions, key_share_groups, psk_modes, extensions
)
VALUES (
    'tk_canonical_antigravity_cli',
    'TokenKey canonical Antigravity CLI TLS profile captured from real agy ClientHello (Go crypto/tls + X25519MLKEM768); extension order is stable (no shuffle). Paired with antigravity/cli/<ver> User-Agent. ja3_hash=03117a8ed39ef02427ebbc39f121275c.',
    false,
    false,
    '[49195,49199,49196,49200,52393,52392,49161,49171,49162,49172,4865,4866,4867]'::jsonb,
    '[4588,4587,4589,29,23,24,25]'::jsonb,
    '[0]'::jsonb,
    '[2308,2309,2310,2052,1027,2055,2053,2054,1025,1281,1537,1283,1539]'::jsonb,
    '["h2","http/1.1"]'::jsonb,
    '[772,771]'::jsonb,
    '[4588,29]'::jsonb,
    '[]'::jsonb,
    '[0,11,65281,23,18,5,10,13,50,16,43,51]'::jsonb
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
