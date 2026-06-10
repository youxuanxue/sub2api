# Antigravity fingerprint changelog

One line per alignment. The canonical truth is the Go constants in
`backend/internal/pkg/antigravity/` (oauth.go / client.go / request_transformer.go) —
this file is just the human-readable history of when each value moved and why.

Capture tool: `ops/antigravity/capture-antigravity-fingerprint.sh` (mitmproxy of a
real Antigravity IDE → diff against the Go constants). Antigravity's load-bearing
fingerprint is HTTP (UA *version*, body `userAgent`, ideType metadata, gl-node
`X-Goog-Api-Client`); JA3 is non-load-bearing and never gates.

Drift-fix recipe (see the `tokenkey-antigravity-fingerprint-alignment` skill):
- UA version bump (most common): edit `DefaultUserAgentVersion` in `oauth.go` +
  the `GetUserAgent()` assertion in `oauth_test.go`, add a row below. Hot-push
  without release via the admin setting `antigravity_user_agent_version`.
- gl-node / ideType / metadata drift: edit the constant in `client.go` + its test.

| date (UTC) | UA version | type | change | note |
|---|---|---|---|---|
| 2026-06-10 | 1.23.2 | baseline | — | capture toolchain added; `DefaultUserAgentVersion=1.23.2`, body `userAgent="antigravity"`, ideType=`ANTIGRAVITY`, `X-Goog-Api-Client=gl-node/22.21.1`. No real-IDE capture yet (IDE not installed locally). |
