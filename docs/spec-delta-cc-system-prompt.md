# Spec Delta — Claude Code system-prompt anchor alignment

**Date:** 2026-06-08
**Skill:** `/tokenkey-cc-fingerprint-alignment`
**Type:** decision — new alignment axis (no cc version / TLS / beta change)
**Registry (source of truth):** `scripts/sentinels/cc-system-prompt.json`

## Why this axis exists

The Claude Code **system prompt** is a load-bearing fingerprint dimension, not
just a payload. Anthropic's upstream client detection keys on the CC identity
banner and the billing-attribution block; when real CC changed its first system
block at **2.1.15**, relays whose copies lagged were rejected with
`client_validation_error 403` (linux.do
[1498051](https://linux.do/t/topic/1498051) /
[1510927](https://linux.do/t/topic/1510927), CRS
[#1093](https://github.com/Wei-Shaw/claude-relay-service/issues/1093)).

In TokenKey the anchors are used in **two directions** and hardcoded in 3+ Go
copies that can silently diverge:

| Location | Symbol | Direction |
|---|---|---|
| `backend/internal/service/claude_code_validator.go` | `claudeCodeSystemPrompts[]` (6) + `claudeCodeBillingHeaderPrefix` | inbound — decide "is this Claude Code" (Dice ≥ 0.5) |
| `backend/internal/service/gateway_service.go` | `claudeCodeSystemPrompt` (injected banner) + `claudeCodePromptPrefixes[]` (4) | outbound — `injectClaudeCodePrompt` spoofs non-CC clients into the Anthropic pool |

Before this change, the alignment skill captured only TLS (ja3) + HTTP headers
(UA / beta / stainless). The system-block dimension was an **unmonitored blind
spot**, and the copies had **no single source / no guard** (validator has 6
templates, gateway has 4 prefixes + 1 banner — already asymmetric).

## What is aligned — anchors only

The full CC system prompt is **dynamic** (cwd / git status / date / env, partly
per-session) and is **never** byte-aligned. Only the stable anchors are tracked:

- **Identity prefixes** — `"You are Claude Code, Anthropic's official CLI for Claude"` and its known variants (Agent SDK, file-search, summarizer).
- **Billing prefix** — `x-anthropic-billing-header` (the first system block CC injects on every real request, more stable than the identity prose).

## Mechanism (two chains, one declared source)

`scripts/sentinels/cc-system-prompt.json` is the hub:

1. **Guard (code == registry).** `scripts/sentinels/check-cc-system-prompt.py`
   (`--check` / `--quiet` / `--selftest`, exit 0/1/2) asserts every Go copy still
   contains the registry's anchor literals and that the canonical banner is
   **byte-identical** across `claude_code_validator.go` and `gateway_service.go`.
   Wired into `scripts/preflight.sh` (so a green local preflight ⇒ green
   `upstream-merge-pr-shape` check — silent upstream deletion is blocked).
   This is a **guard, not a generator**: no `--write`.

2. **Capture (real CC == registry).** The mitm addon
   `ops/anthropic/mitm_cc_http_headers.py` records `system_anchors` (the leading
   ~160 chars of each system block — never full user/session content).
   `ops/anthropic/capture_cc_fingerprint.py` reads the registry's
   `capture_anchors` into the baseline and emits two diff rows:
   - `system.identity_anchor` — **critical/FAIL** if a real capture's system
     blocks match none of the canonical identity prefixes (banner drifted →
     upstream 403 risk).
   - `system.billing_prefix` — **INVESTIGATE** (non-blocking) if absent;
     count_tokens and some sub-requests legitimately omit it.
   - No system blocks captured (TLS-only run) → `missing_capture`/SKIP, never a
     failure.

## Drift procedure (when a real CC update moves an anchor)

1. Confirm with capture evidence (`capture --http` → `check` shows
   `system.identity_anchor` FAIL across a normal `/v1/messages` call).
2. Update `scripts/sentinels/cc-system-prompt.json` **and** the Go copies in the
   same commit; keep the banner byte-identical across both files.
3. Update this record + add a `decision` row to
   `docs/cc-fingerprint-changelog.md`.
4. `./scripts/preflight.sh` green. No release needed (capture + guard + docs
   only; no runtime/compile artifact changes).

## Changed files (this delta)

- `scripts/sentinels/cc-system-prompt.json` (new — registry)
- `scripts/sentinels/check-cc-system-prompt.py` (new — guard)
- `scripts/preflight.sh` (wire guard)
- `ops/anthropic/mitm_cc_http_headers.py` (record `system_anchors`)
- `ops/anthropic/capture_cc_fingerprint.py` (baseline + bundle + diff rows)
- `ops/anthropic/test_capture_cc_fingerprint.py` (5 new tests)
- `.cursor/skills/tokenkey-cc-fingerprint-alignment/SKILL.md` (axis docs)
- `docs/cc-fingerprint-changelog.md` (decision row)

## Validation

```bash
python3 scripts/sentinels/check-cc-system-prompt.py --selftest
python3 scripts/sentinels/check-cc-system-prompt.py            # green
python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic
./scripts/preflight.sh
```
