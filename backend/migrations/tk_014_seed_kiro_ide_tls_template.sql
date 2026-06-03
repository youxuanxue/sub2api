-- Migration: tk_014_seed_kiro_ide_tls_template
-- Seed the canonical Kiro IDE (AWS CodeWhisperer, sixth platform) TLS fingerprint
-- profile so Kiro accounts mask their TLS handshake instead of egressing with a
-- Go-default ClientHello (which stands out and raises the ban risk).
--
-- Values are captured from a REAL Kiro IDE ClientHello by passive pcap
-- (deploy/aws/stage0/tk_canonical_kiro_ide.json; ja3_hash 51bddd625044f75a235ba857ac8b0145,
-- Node 22.22.0). Intentionally distinct from tk_canonical_cc_oauth (Node 24.x) —
-- the JA3 differ, so the profiles must not be shared.
--
-- account.IsTLSFingerprintEnabled() is default-on for Kiro and
-- TLSFingerprintProfileService.ResolveTLSProfile resolves this row BY NAME when no
-- explicit extra.tls_fingerprint_profile_id is bound, so seeding the row is all
-- that is required to activate masking for existing Kiro accounts — no per-account
-- data change.
--
-- Idempotent: ON CONFLICT (name) DO NOTHING — never clobbers an operator edit.
-- Keep the arrays byte-identical to tk_canonical_kiro_ide.json (order-sensitive:
-- cipher and extension order drive the JA3).
--
-- UPDATING after a Kiro IDE release shifts the fingerprint (the two encodings of
-- the profile — this seed and deploy/aws/stage0/tk_canonical_kiro_ide.json — are
-- NOT auto-synced; the JSON is the capture/diff baseline + provenance, this row is
-- the runtime source):
--   1. re-capture and `ops/kiro/capture_kiro_fingerprint.py emit-profile` to refresh
--      the JSON, then
--   2. propagate to the live DB row by EITHER editing the profile in the admin TLS
--      fingerprint UI, OR shipping a follow-up migration that re-seeds with
--      `ON CONFLICT (name) DO UPDATE SET ...` (this DO NOTHING seed only sets the
--      initial value and will not re-apply).
-- emit-profile alone updates only the JSON; step 2 is required or the wire keeps
-- the old JA3.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

INSERT INTO tls_fingerprint_profiles (
    name, description, enable_grease,
    cipher_suites, curves, point_formats, signature_algorithms,
    alpn_protocols, supported_versions, key_share_groups, psk_modes, extensions
)
VALUES (
    'tk_canonical_kiro_ide',
    'Canonical Kiro IDE (AWS CodeWhisperer) TLS profile, captured by passive pcap from a real Kiro IDE ClientHello (Node 22.22.0). ja3_hash=51bddd625044f75a235ba857ac8b0145, no GREASE. Distinct from tk_canonical_cc_oauth (Node 24.x). See ops/kiro/ + docs/accounts/kiro-tls-fingerprint-alignment-design.md.',
    false,
    '[4865,4866,4867,49199,49195,49200,49196,49191,52393,52392,49161,49171,49162,49172,156,157,47,53]'::jsonb,
    '[29,23,24]'::jsonb,
    '[0]'::jsonb,
    '[1027,2052,1025,1283,2053,1281,2054,1537,513]'::jsonb,
    '[]'::jsonb,
    '[772,771]'::jsonb,
    '[29]'::jsonb,
    '[1]'::jsonb,
    '[0,23,65281,10,11,35,13,51,45,43,21]'::jsonb
)
ON CONFLICT (name) DO NOTHING;
