# Spec Delta — Claude Code cc 2.1.161 HTTP fingerprint alignment

**Date:** 2026-06-02
**Skill:** `/tokenkey-cc-fingerprint-alignment`
**Type:** HTTP-only (User-Agent / canonical version); TLS ja3 unchanged
**Capture egress:** cc0 SOCKS chain → 16.147.170.3
**Capture bundle:** `.tls_list/20260602T221811Z-cc-capture.bundle.json`

## Ground truth (captured)

| Field | Captured (real cc) | Prior baseline |
|---|---|---|
| cc version | 2.1.161 | 2.1.160 |
| User-Agent (mimic) | `claude-cli/2.1.161 (external, sdk-cli)` | `claude-cli/2.1.160 (external, sdk-cli)` |
| User-Agent (canonical) | `claude-cli/2.1.161 (external, sdk-cli)` | `claude-cli/2.1.160 (external, sdk-cli)` |
| x-stainless-package-version | 0.94.0 | 0.94.0 |
| anthropic-beta (haiku) | 8 tokens (structured-outputs variant) — **A/B split still observed, see below** | 8 tokens |
| anthropic-beta (sonnet/opus) | 10 / 11 tokens, unchanged | 10 / 11 tokens |
| TLS ja3_hash | d871d02cecbde59abbf8f4806134addf | d871d02cecbde59abbf8f4806134addf |

Single-capture repo `diff`/`check`: `match=6 mismatch=2` — the two mismatches are
`canonical.user_agent_version` and `mimic.cli_version` (both 2.1.160 → 2.1.161);
TLS ja3 hash + raw, stainless package version, and both beta sets all matched.

## Comprehensive beta consistency — haiku A/B split persists (server-side gray release)

`ops/anthropic/capture-http-comprehensive.sh` over **11 haiku** requests returned
**2 unique beta headers**; sonnet (×6) and opus (×2) were each **1 unique header**
(no split). Both haiku variants came from the *same* cc 2.1.161 binary, so this is
the **same server-side per-request A/B gray release** first recorded for cc 2.1.160 —
not a client-version difference. The TokenKey mimic uses a fixed beta set and cannot
(and should not) replicate a per-request server-side toggle.

| Variant | Count (of 11) | Distinguishing tokens |
|---|---|---|
| **A** (current TK baseline) | 8 | `…advisor-tool-2026-03-01, structured-outputs-2025-12-15, cache-diagnosis-2026-04-07` |
| **B** | 3 | `…claude-code-20250219, advisor-tool-2026-03-01, extended-cache-ttl-2025-04-11, cache-diagnosis-2026-04-07` |

Both share the prefix `oauth-2025-04-20, interleaved-thinking-2025-05-14,
thinking-token-count-2026-05-13, context-management-2025-06-27,
prompt-caching-scope-2026-01-05`. Variant B swaps `structured-outputs-2025-12-15`
for `claude-code-20250219` + `extended-cache-ttl-2025-04-11` — identical to the
2.1.160-round split.

**Decision:** keep `HaikuBetaHeader` / `FullClaudeCodeHaikuMimicryBetas` on **Variant A**
(`structured-outputs`), which is (1) the majority variant this round (8/11) and (2) the
already-shipped TokenKey baseline. Per the skill's hard rule, a `WARN`/split is
**recorded** here but does **not** trigger a beta-constant change without single-variant
capture evidence. Re-evaluate if a future capture shows Variant B becoming the stable
majority.

## Changed files

- `backend/internal/pkg/claude/constants.go` — `CLICurrentVersion`, `DefaultHeaders["User-Agent"]`
- `backend/internal/service/identity_service.go` — `defaultFingerprint.UserAgent`
- `backend/internal/service/identity_service_tk_canonical_http.go` — `DefaultClaudeCodeUserAgentVersion`
- `deploy/aws/stage0/anthropic-http-mimicry-baselines.json` — `cc_version` (runtime truth source)
- `deploy/aws/stage0/tk_canonical_cc_oauth.json` — `observed.user_agent` (auto-regen via `check-cc-version-sync.py --write`)
- `ops/stage0/smoke_lib.sh` — `smoke_default_claude_user_agent` default UA (auto-regen)

## Validation

- `go test -tags=unit ./internal/pkg/claude/... -run TestFullClaudeCode`
- `python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic`
- `python3 scripts/sentinels/check-cc-version-sync.py` (exit 0, all copies consistent)
- `./scripts/preflight.sh`

## Runtime apply (post-merge)

- `bash ops/anthropic/cc_fingerprint_apply_http_runtime.sh` (no release needed)
- `constants.go` compile default catches up on next release
