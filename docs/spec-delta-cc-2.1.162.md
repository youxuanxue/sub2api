# Spec Delta — Claude Code cc 2.1.162 HTTP fingerprint alignment

**Date:** 2026-06-04
**Skill:** `/tokenkey-cc-fingerprint-alignment`
**Type:** HTTP-only (User-Agent / canonical version); TLS ja3 unchanged
**Capture egress:** cc0 SOCKS chain → 16.147.170.3
**Capture bundles:** `.tls_list/20260603T232504Z-cc-capture.bundle.json` (single), `.tls_list/20260603T232545Z-cc-capture.bundle.json` (comprehensive)

## Ground truth (captured)

| Field | Captured (real cc) | Prior baseline |
|---|---|---|
| cc version | 2.1.162 | 2.1.161 |
| User-Agent (mimic) | `claude-cli/2.1.162 (external, sdk-cli)` | `claude-cli/2.1.161 (external, sdk-cli)` |
| User-Agent (canonical) | `claude-cli/2.1.162 (external, sdk-cli)` | `claude-cli/2.1.161 (external, sdk-cli)` |
| x-stainless-package-version | 0.94.0 | 0.94.0 |
| anthropic-beta (haiku) | 8 tokens (structured-outputs variant) — **A/B split still observed, see below** | 8 tokens |
| anthropic-beta (sonnet/opus) | 10 / 11 tokens, unchanged | 10 / 11 tokens |
| TLS ja3_hash | d871d02cecbde59abbf8f4806134addf | d871d02cecbde59abbf8f4806134addf |

Single-capture repo `diff`/`check`: `match=5 mismatch=2 needs_investigation=1` — the two
mismatches are `canonical.user_agent_version` and `mimic.cli_version` (both 2.1.161 → 2.1.162);
TLS ja3 hash + raw and stainless package version matched; sonnet beta matched; the haiku beta
field is now reported as `needs_investigation` (bimodal, baseline matches the majority variant)
rather than a hard mismatch — see [youxuanxue/sub2api#429](https://github.com/youxuanxue/sub2api/issues/429).

## Comprehensive beta consistency — haiku A/B split persists (server-side gray release)

`ops/anthropic/capture-http-comprehensive.sh` over **11 haiku** requests returned
**2 unique beta headers**; sonnet (×6) and opus (×2) were each **1 unique header**
(no split). Both haiku variants came from the *same* cc 2.1.162 binary, so this is
the **same server-side per-request A/B gray release** recorded for cc 2.1.160 / 2.1.161 —
not a client-version difference. The TokenKey mimic uses a fixed beta set and cannot
(and should not) replicate a per-request server-side toggle.

| Variant | Count (of 11) | Distinguishing tokens |
|---|---|---|
| **A** (current TK baseline) | 8 | `…advisor-tool-2026-03-01, structured-outputs-2025-12-15, cache-diagnosis-2026-04-07` |
| **B** | 3 | `…claude-code-20250219, advisor-tool-2026-03-01, advanced-tool-use-2025-11-20, extended-cache-ttl-2025-04-11, cache-diagnosis-2026-04-07` |

Both share the prefix `oauth-2025-04-20, interleaved-thinking-2025-05-14,
thinking-token-count-2026-05-13, context-management-2025-06-27,
prompt-caching-scope-2026-01-05`. Variant B swaps `structured-outputs-2025-12-15`
for `claude-code-20250219` + `advanced-tool-use-2025-11-20` + `extended-cache-ttl-2025-04-11`
(i.e. the Sonnet agentic set minus `effort`) — identical to the 2.1.160 / 2.1.161 split.

**Decision:** keep `HaikuBetaHeader` / `FullClaudeCodeHaikuMimicryBetas` on **Variant A**
(`structured-outputs`), which is (1) the majority variant this round (8/11) and (2) the
already-shipped TokenKey baseline. Per the skill's hard rule and the #429 bimodal guard,
a split is **recorded** here and surfaces as `INVESTIGATE` (non-blocking), but does **not**
trigger a beta-constant change without single-variant capture evidence. Re-evaluate (and
characterize A vs B by request purpose / tool presence per #429) if a future capture shows
Variant B becoming the stable majority.

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
