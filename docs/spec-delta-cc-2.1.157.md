# spec-delta: cc 2.1.157 UA alignment

## Background

cc 2.1.157 mitm + TLS capture (2026-05-30, cc0 → gost → socks) shows **UA-only** drift from 2.1.156. TLS ClientHello unchanged (`ja3_hash=d871d02cecbde59abbf8f4806134addf`). Beta sets for Sonnet/Opus and dominant Haiku mimicry (structured-outputs path) unchanged.

`capture-http-comprehensive.sh` (3×3×2): Sonnet and Opus stable (1 unique beta each). Haiku **bimodal** (structured-outputs vs claude-code+extended-cache-ttl) — same gray split as 2.1.156; PR keeps dominant structured-outputs set in `FullClaudeCodeHaikuMimicryBetas()` and documents the WARN in evidence only.

### edge-uk1 ops error correlation (2026-05-30)

Triggered by edge-uk1 Admin error spike investigation. Live triage (`ops-error-triage.sh`, 30 min window on `/v1/messages`):

| Category | Count | final status |
|---|---|---|
| `signature_preempt_applied` / `thinking_blocks_stripped` | 39 | 200 |
| `signature_error` (thinking blocks cannot be modified) | 7 | 200 (recovered) |
| `signature_preempt_armed` | 3 | 200 |
| `No available accounts` | 1 | 503 |

**Conclusion:** the uk1 spike is **not TLS fingerprint drift**. Account `en-ld-ls-5-1-a` uses `tk_canonical_cc_oauth` (ja3 already aligned). Errors are TokenKey's signature preempt / retry recovery logging for long Claude Code multi-turn sessions with modified thinking blocks — user-facing outcome was 200 except one 503 when the sole account was temporarily marked non-schedulable (operator intent). This PR only closes the compile-time UA gap 2.1.156 → 2.1.157.

## Delta

### MODIFIED

- `DefaultClaudeCodeUserAgentVersion` / `CLICurrentVersion` / mimic + canonical observed UA: **2.1.156 → 2.1.157**.
- `anthropic-http-mimicry-baselines.json` `cc_version` field.
- Smoke default UA, sentinel rationale, capture baseline test.

### NOT changed

- TLS ja3 / `tk_canonical_cc_oauth` cipher profile bodies.
- `FullClaudeCodeMimicryBetas()` / `FullClaudeCodeHaikuMimicryBetas()` token lists.
- Signature preempt thresholds or thinking-block retry logic.

## Scenarios

### Core positive

1. Given cc 2.1.157 bundle, when `capture_cc_fingerprint.py check --bundle`, then all critical HTTP+TLS fields match.
2. Given OAuth forward on canonical path, when UA is built, then wire version is 2.1.157.

### Core negative

1. Given stale 2.1.156 constants, when check runs against 2.1.157 capture, then UA mismatches fail the gate.

### Regression

- `TestFullClaudeCodeMimicryBetas_MatchesCC0157*` pass.
- TLS `check-tls` on capture bundle: OK.

## Validation

```bash
go test -tags=unit ./internal/pkg/claude/... -run TestFullClaudeCode
python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic
bash ops/anthropic/capture-cc-fingerprint.sh capture --http
python3 ops/anthropic/capture_cc_fingerprint.py check --bundle .tls_list/20260530T004309Z-cc-capture.bundle.json
bash ops/anthropic/capture-cc-fingerprint.sh check-tls --bundle .tls_list/20260530T004309Z-cc-capture.bundle.json
./scripts/preflight.sh
```

Post-merge runtime sync (no release required for HTTP UA):

```bash
bash ops/anthropic/cc_fingerprint_apply_http_runtime.sh
```

Evidence: `.tls_list/20260530T004309Z-cc-capture.bundle.json`, `.tls_list/20260530T004332Z-http-multi.log` (comprehensive; haiku WARN bimodal).
