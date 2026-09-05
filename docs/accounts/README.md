# Account and upstream-account references

This directory is for account onboarding, OAuth stability, and fingerprint
baseline references. Raw vendor pricing captures live under
[`../evidence/pricing/`](../evidence/pricing/).

| File | Topic |
| --- | --- |
| [`anthropic-oauth-edge-guidelines.md`](anthropic-oauth-edge-guidelines.md) | Anthropic OAuth edge TLS and tier guidance |
| [`anthropic-oauth-edge-stability-baseline-index.md`](anthropic-oauth-edge-stability-baseline-index.md) | Anthropic OAuth edge stability baseline index |

Kiro TLS fingerprint alignment is owned by the
`tokenkey-kiro-fingerprint-alignment` skill and
`backend/data/tls/tk_canonical_kiro_cli.json` (seeded by
`tk_014_seed_kiro_ide_tls_template.sql`).
