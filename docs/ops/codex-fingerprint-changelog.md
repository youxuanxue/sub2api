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
- **TLS field change** → refresh `deploy/aws/stage0/tk_canonical_codex_cli.json`
  and add a forward migration that updates the same-named DB row. Never edit a
  published migration.
- Raw capture bundles, pcaps and logs are never committed.

## Log

| codex-cli version | date (UTC) | runtime | identity | note |
|---|---|---|---|---|
| 0.167.0 | 2026-11-06 | rustls 0.23.46, reqwest 0.12.28, h2 0.4.19 | JA4 `t13d1011h2_61a7ad8aa9b6_0d308c48d2a3`; JA3 with sorted extensions `082240ccb778985f253b420029540a34`; WSS hello has no ALPN (`t13d101000_…`) | First capture, 22 chatgpt.com handshakes. rustls shuffles extension order per connection (`shuffle_extensions=true`). Key share leads with X25519MLKEM768. HTTP/2 SETTINGS and window sizes match codex; Go keeps pseudo-header order `a,m,p,s` (codex sends `m,s,a,p`). Authenticated live test on one prod OAuth account through its socks5h proxy, 2026-11-07: HTTPS HTTP/2 200 ×2 and WSS 101, all `response.completed`, no 403 or `cf-mitigated`. Migration `tk_109_codex_cli_tls_profile`. |
