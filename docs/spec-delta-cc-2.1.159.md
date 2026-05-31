# Spec Delta — Claude Code cc 2.1.159 HTTP fingerprint alignment

**Date:** 2026-06-01
**Skill:** `/tokenkey-cc-fingerprint-alignment`
**Type:** HTTP-only (User-Agent / canonical version); TLS ja3 unchanged
**Capture egress:** cc0 SOCKS chain → 16.147.170.3
**Capture bundle:** `.tls_list/20260601-143052-cc-capture.bundle.json`

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
sonnet ×3, opus ×2 — each family **1 unique beta header**, no split/canary. UA stable at
`claude-cli/2.1.159 (external, cli)` across all requests.

## Changed files

- `backend/internal/pkg/claude/constants.go` — `MimicClaudeCodeCLIVersion`, `CanonicalClaudeCodeVersion`, `DefaultClaudeCodeUserAgentVersion`
- `backend/internal/service/identity_service_tk_canonical_http.go` — `canonicalCCVersion`, `fallbackCanonicalCCVersion`
- `backend/internal/service/identity_service.go` — `mimicClaudeCodeVersion`
- `backend/internal/pkg/claude/constants_test.go` — expected UA strings
- `deploy/aws/stage0/anthropic-http-mimicry-baselines.json` — runtime truth source

Sentinel registry (`scripts/sentinels/gateway-tk.json`) and smoke lib
(`ops/stage0/smoke_lib.sh`) carry no pinned cc version string — no change needed.

## Validation

- `go test -tags=unit ./internal/pkg/claude/... -run TestFullClaudeCode`
- `python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic`
- `./scripts/preflight.sh`

## Runtime apply (post-merge)

- `bash ops/anthropic/cc_fingerprint_apply_http_runtime.sh` (no release needed)
- `constants.go` compile default catches up on next release
