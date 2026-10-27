# Kiro CLI fingerprint alignment — changelog

Single rolling log of Kiro CLI fingerprint alignments produced by
`/tokenkey-kiro-fingerprint-alignment`. **Append one row per alignment.**

Kiro is the only client whose evidence mode is `wire_tls_http`: the real,
logged-in `kiro-cli` plus a mitm collector is the sole ground truth, across four
evidence lanes (TLS / HTTP / protocol / auth). Release metadata and version
strings never substitute for a bundle.

Convention:

- **Version-only drift** (wire HTTP identity moved, TLS runtime projection
  unchanged) → bump the single compile owner
  `backend/internal/pkg/kiro/constants.go` `DefaultKiroCLIVersion`, refresh
  `deploy/aws/stage0/tk_canonical_kiro_cli.json` via `emit-profile`, add **one
  row here** and stop. Record the runtime digest so the next reader can tell at a
  glance whether a `tls_fingerprint_profiles` migration was required.
- **TLS runtime field change** (digest differs) → additionally add a forward
  migration projecting onto the same-named DB row. Never edit a published
  migration.

Raw capture bundles, token caches, pcaps and mitm logs are never committed.

## Log

| kiro-cli version | date (UTC) | type | runtime digest | note |
|---|---|---|---|---|
| 2.34.2 | 2026-10-27 | wire HTTP, version-only | `97340b4d…` unchanged | Issue #2937 signaled upstream 2.34.1 over pin 2.34.0; live `kiro-cli update` on a logged-in Mac Builder ID install landed **2.34.2**. Real mitm bundle stamp `20261027T080349Z` (`capture --samples 3`); all four evidence lanes valid. Only `md/appVersion-` moved — `aws-sdk-rust/1.3.10`, `api/codewhispererruntime/0.1.10231`, `lang/rust/1.94.1` unchanged. TLS runtime projection digest identical before/after (observed ClientHello extension-order/JA3 churn is rustls `shuffle_extensions`, not semantic drift), so **no migration was needed**. Closes #2937. |
| 2.34.0 | 2026-10-25 | wire HTTP, version-only | `97340b4d…` unchanged | 2.33.1 → 2.34.0 from a real logged-in `kiro-cli` + mitm bundle; all four evidence lanes valid. Only `md/appVersion-` moved — `aws-sdk-rust/1.3.10`, `api/codewhispererruntime/0.1.10231`, `lang/rust/1.94.1` unchanged. TLS runtime projection digest identical before/after (the `observed` churn is rustls per-ClientHello extension permutation, not semantic drift), so **no migration was needed**. Also closed a gate blind spot: `check` validated four lanes but computed its exit code from the TLS projection alone, so this very bundle's `md/appVersion-2.34.0` against pin `2.33.1` reported `aligned`/0; `http_identity_drift` now reconciles wire HTTP identity against the compile owner (drift → 1, unparsable UA or missing owner → 3, never `aligned`). Closes #2883. |
