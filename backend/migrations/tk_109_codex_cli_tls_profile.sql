-- Migration: tk_109_codex_cli_tls_profile
--
-- Seed the real Codex CLI TLS ClientHello as tk_canonical_codex_cli. Captured
-- 2026-11-06 from local codex-cli 0.167.0 (Rust reqwest 0.12 + rustls 0.23 +
-- aws-lc-rs) through a local CONNECT capture endpoint (22 chatgpt.com samples).
-- rustls shuffles extension order per connection → shuffle_extensions=true;
-- JA4 t13d1011h2_61a7ad8aa9b6_0d308c48d2a3 is the stable identity. OpenAI OAuth
-- accounts use it only when extra.enable_tls_fingerprint=true. Safe to re-run.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

INSERT INTO tls_fingerprint_profiles (
    name, description, enable_grease, shuffle_extensions,
    cipher_suites, curves, point_formats, signature_algorithms,
    alpn_protocols, supported_versions, key_share_groups, psk_modes, extensions
)
VALUES (
    'tk_canonical_codex_cli',
    'TokenKey canonical Codex CLI TLS profile captured from real codex ClientHello (rustls + X25519MLKEM768, extension order shuffled per connection). Paired with codex_* User-Agent. ja4=t13d1011h2_61a7ad8aa9b6_0d308c48d2a3 ja3_sorted=082240ccb778985f253b420029540a34.',
    false,
    true,
    '[4866,4865,4867,49196,49195,52393,49200,49199,52392,255]'::jsonb,
    '[4588,29,23,24]'::jsonb,
    '[0]'::jsonb,
    '[1283,1027,1539,2055,2054,2053,2052,1537,1281,1025,2308,2309,2310]'::jsonb,
    '["h2","http/1.1"]'::jsonb,
    '[772,771]'::jsonb,
    '[4588,29]'::jsonb,
    '[1]'::jsonb,
    '[0,5,10,11,13,16,23,35,43,45,51]'::jsonb
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
