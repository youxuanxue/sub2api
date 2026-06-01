# Spec Delta — Claude Code cc 2.1.159 HTTP fingerprint alignment

**Date:** 2026-06-01
**Skill:** `/tokenkey-cc-fingerprint-alignment`
**Type:** HTTP-only (User-Agent / canonical version); TLS ja3 unchanged
**Capture egress:** cc0 SOCKS chain → 16.147.170.3
**Capture bundle:** `.tls_list/20260531T232715Z-cc-capture.bundle.json`

## Ground truth (captured)

| Field | Captured (real cc) | Prior baseline |
|---|---|---|
| cc version | 2.1.159 | 2.1.158 |
| User-Agent (mimic) | `claude-cli/2.1.159 (external, cli)` | `claude-cli/2.1.158 (external, cli)` |
| User-Agent (canonical) | `claude-cli/2.1.159 (external, sdk-cli)` | `claude-cli/2.1.158 (external, sdk-cli)` |
| x-stainless-package-version | 0.94.0 | 0.94.0 |
| anthropic-beta (haiku) | 8 tokens, unchanged | 8 tokens |
| anthropic-beta (sonnet/opus) | 10 tokens, unchanged | 10 tokens |
| TLS ja3_hash | d871d02cecbde59abbf8f4806134addf | d871d02cecbde59abbf8f4806134addf |

Comprehensive beta consistency (`ops/anthropic/capture-http-comprehensive.sh`): haiku ×3,
sonnet ×6, opus ×2 — each family **1 unique beta header**, no split/canary. UA stable at
`claude-cli/2.1.159 (external, cli)` across all requests.

## Changed files

- `backend/internal/pkg/claude/constants.go` — `CLICurrentVersion`, `DefaultHeaders["User-Agent"]`, capture-date / cc-version comments
- `backend/internal/service/identity_service.go` — `defaultFingerprint.UserAgent`
- `backend/internal/service/identity_service_tk_canonical_http.go` — `DefaultClaudeCodeUserAgentVersion`
- `backend/internal/service/gateway_context_management_test.go` — cc-version reference in test comments
- `deploy/aws/stage0/anthropic-http-mimicry-baselines.json` — `cc_version` (runtime truth source)
- `deploy/aws/stage0/tk_canonical_cc_oauth.json` — `observed.user_agent` + capture-range note in `description`
- `ops/anthropic/test_capture_cc_fingerprint.py` — `canonical_http.default_version` baseline assertion
- `ops/stage0/smoke_lib.sh` — `smoke_default_claude_user_agent` default UA
- `scripts/sentinels/gateway-tk.json` — mimicry-beta sentinel `rationale` cc-version reference

## Validation

- `go test -tags=unit ./internal/pkg/claude/... -run TestFullClaudeCode`
- `python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic`
- `./scripts/preflight.sh`

## Runtime apply (post-merge)

- `bash ops/anthropic/cc_fingerprint_apply_http_runtime.sh` (no release needed)
- `constants.go` compile default catches up on next release
