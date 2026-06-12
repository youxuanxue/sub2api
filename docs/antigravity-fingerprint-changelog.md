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
| 2026-06-11 | 1.23.2 | toolchain-verify | — | First real-client capture via the **Antigravity CLI** (`agy` 1.0.7, Go) through mitmproxy→gost, not the IDE. Confirms body `userAgent="antigravity"` ✓ and ideType=`ANTIGRAVITY` ✓. CLI is a distinct client from the IDE the baseline mirrors: UA `antigravity/cli/1.0.7 darwin/arm64`, no `gl-node` header (Go, not Node), endpoint `daily-cloudcode-pa.googleapis.com`. No baseline change — IDE remains the mimic target; see skill § "用 Antigravity CLI（`agy`）采集" for the Go-CLI login-keychain trust path. |
| 2026-06-12 | 1.23.2 | ide-validate | — | Installed the real **Antigravity IDE 2.0.11** and validated the IDE-targeted constants **without an on-wire capture** (its Go `language_server` direct-dials Google, bypassing every HTTP proxy — proxy-env mitm cannot intercept it; only a system TUN can, which is heavy). The IDE's own spawn command is authoritative: `language_server --override_user_agent_name antigravity --subclient_type hub --override_ide_version 2.0.11 --cloud_code_endpoint https://daily-cloudcode-pa.googleapis.com`. Validation: TK's UA *format* `antigravity/%s windows/amd64` is **correct** (no `/cli/` segment, unlike the CLI); `gl-node/22.21.1`, ideType/ideName, `PLATFORM_UNSPECIFIED`, pluginType all confirmed present in the real `language_server` binary. **Only drift = the version**: TK's `1.23.2` is the binary's un-overridden default; the shipping IDE puts `2.0.11` on the wire via `--override_ide_version` (= app version, changes every IDE auto-update). **No constant bump** — operators track the shipping version via the admin hot-push `antigravity_user_agent_version`, not a compiled change that would diverge from upstream for a moving target. |
