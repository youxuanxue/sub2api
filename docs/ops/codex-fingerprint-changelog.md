# Codex CLI TLS fingerprint — changelog

Rolling log of Codex CLI TLS captures behind `tk_canonical_codex_cli`. **Append
one row per capture.**

OpenAI OAuth accounts use this profile only when the account sets
`extra.enable_tls_fingerprint=true` (opt-in per account). Accounts without the
flag keep Go crypto/tls and the same `httpUpstream.Do` call as before.

Convention:

- Capture with the local `codex` CLI through a local CONNECT capture endpoint,
  using a throwaway `CODEX_HOME` and a dummy key. Never send real tokens to a
  third-party echo service.
- The capture is mechanical: `bash ops/openai/capture-codex-fingerprint.sh
  tls-capture --samples 3` writes an ignored bundle under `.codex_tls/` and runs
  `tls-check` against `deploy/aws/stage0/tk_canonical_codex_cli.json`. The
  runtime digest is the SHA-256 of the shared runtime projection
  (`ops/kiro/capture_kiro_fingerprint.py`, extensions sorted because rustls
  shuffles them).
- **`aligned`** → nothing to do; no row is needed.
- **`version_only`** (codex version moved, runtime digest and HTTP/2 / WSS wire
  unchanged) → `tls-emit-profile --bundle <bundle>` to refresh the observed
  version, add **one row here** with the unchanged digest, and stop. No
  migration.
- **`tls_drift` with a changed runtime digest** (cipher, extension set, groups,
  key share, sig algs, ALPN, PSK modes, versions or GREASE) → `tls-emit-profile`,
  add a **new forward migration** that updates the same-named DB row, and add a
  row here with the new digest. Never edit a published migration.
- **`tls_drift` with the digest unchanged** (HTTP/2 SETTINGS / WINDOW_UPDATE /
  pseudo-header order, or WSS hello/path) → no migration; fix the Go wire owner
  (`backend/internal/repository/http_upstream_tk_codex_h2.go` for HTTP/2,
  `backend/internal/service/openai_ws_client_tk_tls.go` for WSS), then
  `tls-emit-profile` and a row here.
- Raw capture bundles, pcaps, CA keys and logs are never committed.

## Log

| codex-cli version | date (UTC) | runtime | identity | note |
|---|---|---|---|---|
| 0.167.2 | 2026-11-12 | unchanged; runtime digest `bae02ba1…` | JA4 `t13d1011h2_61a7ad8aa9b6_0d308c48d2a3`, WSS `t13d101000_…`; HTTP/2 `2:0;4:2097152;5:16384;6:16384\|5177345\|0\|m,s,a,p` | `tls-capture --samples 3` of installed `codex-cli 0.167.2`: 6 HTTPS + 3 WSS `chatgpt.com` ClientHellos, every one with a distinct extension order. `tls-check` = `version_only`. Runtime digest and HTTP/2 / WSS wire match 0.167.0, so no migration. Closes #3165. |
| 0.167.0 | 2026-11-08 | unchanged; runtime digest `bae02ba1…` | JA4 `t13d1011h2_61a7ad8aa9b6_0d308c48d2a3`, WSS `t13d101000_…`; HTTP/2 `2:0;4:2097152;5:16384;6:16384\|5177345\|0\|m,s,a,p` | First mechanical `tls-capture --samples 3` run: 6 HTTPS + 3 WSS chatgpt.com ClientHellos, every one with a distinct extension order. `tls-check` = `aligned`. Profile and migration not changed. |
| 0.167.0 | 2026-11-06 | rustls 0.23.46, reqwest 0.12.28, h2 0.4.19 | JA4 `t13d1011h2_61a7ad8aa9b6_0d308c48d2a3`; JA3 with sorted extensions `082240ccb778985f253b420029540a34`; WSS hello has no ALPN (`t13d101000_…`) | First capture, 22 chatgpt.com handshakes. rustls shuffles extension order per connection (`shuffle_extensions=true`). Key share leads with X25519MLKEM768. HTTP/2 SETTINGS and window sizes match codex; Go keeps pseudo-header order `a,m,p,s` (codex sends `m,s,a,p`). Authenticated live test on one prod OAuth account through its socks5h proxy, 2026-11-07: HTTPS HTTP/2 200 ×2 and WSS 101, all `response.completed`, no 403 or `cf-mitigated`. Migration `tk_109_codex_cli_tls_profile`. |
