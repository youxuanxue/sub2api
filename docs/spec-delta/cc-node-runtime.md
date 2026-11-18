# Claude Code Node runtime and TLS alignment

## Background

Real Claude Code 2.1.327 captures on 2026-11-18 show Node v26.12.1 and a
ClientHello different from the previous Node v26.3.0 baseline. Version-only
alignment would leave the HTTP runtime header and TLS profile inconsistent.

## Delta

- Update `tk_canonical_cc_oauth` from JA3
  `d871d02cecbde59abbf8f4806134addf` to `1523504b38f0fae0d881d4b6554aac1b`.
  The captured profile adds supported group 4588, sends key shares 4588 and 29,
  adds signature algorithms 2055 and 1539, and removes extension 21 (padding).
- Synchronize the tier baseline and embedded runtime profile with the canonical
  capture. Preserve the stable profile name.
- Update all canonical/fallback Stainless runtime headers to `v26.12.1`.
  Package version remains `0.128.0`.

## Evidence and limits

Two independent real CLI collector runs agree on all TLS profile fields:
`.tls_list/20261118T043513Z-cc-capture.tls-observed.json` and
`.tls_list/20261118T044106Z-cc-capture.tls-observed.json` (local, ignored).
The collector also observed the Stainless runtime header.

The first run additionally captured Haiku and Sonnet `/v1/messages` requests
through `http_capture_invoke.sh` using the mechanically classified
`third_party_token` cohort. Runtime headers and system identity anchors were
observed; OAuth beta evidence is `NOT_OBSERVED`. No OAuth beta constants change.
The billing prefix was absent and remains an investigation item for a future
first-party OAuth capture. The upstream proxy returned CONNECT 503 errors;
request evidence does not prove successful model responses.

Raw captures and credentials remain local. This PR changes repository baselines;
production TLS profile apply/parity verification remains a separate operation.

## Scenarios and validation

- Real CLI capture checks match the reviewed JA3/raw and runtime headers.
- `TestClaudeCLIProfileClientHelloMatchesCapture` replays the production uTLS
  handshake and checks JA3, ordered extensions, groups, signatures, key shares,
  ALPN and supported versions against the committed capture.
- `test_tls_runtime_baseline_matches_canonical_capture` detects divergence
  between the canonical profile, embedded runtime baseline and HTTP pins.
- Run the CC capture-engine suite, identity consistency tests, sentinels,
  preflight and full `make test` before delivery.
