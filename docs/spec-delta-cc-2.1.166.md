# spec-delta: cc fingerprint alignment 2.1.165 → 2.1.166

## Background

Claude Code patch `2.1.166` shipped. `/tokenkey-cc-fingerprint-alignment` capture
against live cc (cc0-here, egress `16.147.170.3`) on 2026-06-06 shows this is a
**pure User-Agent version bump** — no TLS drift, no actionable beta drift.

## Capture evidence (ground truth = real cc 2.1.166)

| field | tokenkey (2.1.165 baseline) | captured (2.1.166) | verdict |
|---|---|---|---|
| `tls.ja3_hash` | unchanged | unchanged | ✅ OK (no TLS profile change) |
| `tls.ja3_raw` | unchanged | unchanged | ✅ OK |
| `canonical.user_agent_version` | 2.1.165 | **2.1.166** | ❌ bump |
| `mimic.cli_version` | 2.1.165 | **2.1.166** | ❌ bump |
| `canonical/mimic.stainless_package_version` | 0.94.0 | 0.94.0 | ✅ OK |
| `betas.sonnet_mimicry` | (10 betas) | same | ✅ OK |
| `betas.haiku_mimicry` | (8 betas) | **bimodal** | ⚠️ INVESTIGATE (not actionable) |

### haiku beta — bimodal A/B (pre-existing #429, NOT changed here)

`capture-http-comprehensive.sh`: **11 haiku requests, 2 unique beta headers**,
baseline matches variant **8/11**. The minority variant (3/11) is the sonnet-shaped
set with `claude-code-20250219` + `advanced-tool-use-2025-11-20` +
`extended-cache-ttl-2025-04-11`. This is the known server-side A/B gray release
tracked in youxuanxue/sub2api#429 — `check` returns exit 0 (`needs_investigation`,
not `has_actionable_mismatch`). The canonical `HaikuBetaHeader` is **left
unchanged**; this PR does NOT touch any beta constant. Opus (2/2) and sonnet (6/6)
betas single-valued and OK.

## Delta

### MODIFIED (UA version only — via `check-cc-version-sync.py --write`)

Single hand-edited source: `deploy/aws/stage0/anthropic-http-mimicry-baselines.json`
`cc_version` `2.1.165 → 2.1.166`. The sync script mechanically rewrote the derived
copies (7 total, incl. embedded byte-sync):

- `backend/internal/pkg/claude/constants.go` — `CLICurrentVersion` + `DefaultHeaders["User-Agent"]`
- `backend/internal/service/identity_service.go` — `defaultFingerprint.UserAgent`
- `backend/internal/service/identity_service_tk_canonical_http.go` — `DefaultClaudeCodeUserAgentVersion`
- `deploy/aws/stage0/tk_canonical_cc_oauth.json` — `observed.user_agent`
- `ops/stage0/smoke_lib.sh` — dead snapshot
- `backend/internal/baseline/anthropic-http-mimicry-baselines.json` — embedded copy (byte-identical to deploy source)

### NOT CHANGED

- TLS: `tk_canonical_cc_oauth.json` ja3 profile — unchanged (no ClientHello drift).
- Betas: no `constants.go` beta constant touched (haiku bimodal is #429, baseline still valid).

## Rollout

UA has a runtime path: after merge run `ops/anthropic/cc_fingerprint_apply_http_runtime.sh`
(settings `claude_code_user_agent_version` + Redis fingerprint DEL) — no image release
required, **provided the target node binary carries the mimicry-selfheal monotonic
ratchet** (v1.7.72 and earlier do NOT — their reconciler rolls a runtime UA bump back
to the embedded value within one tick). For older binaries the only durable path is
the next scheduled release, where the embedded baseline updates with the image and the
reconciler converges automatically.
