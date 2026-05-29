# spec-delta: cc 2.1.156 UA alignment

## Background

cc 2.1.156 mitm + TLS capture (2026-05-29, cc0 → gost → socks) shows **UA-only** drift from 2.1.154. TLS ClientHello unchanged (same ja3_hash). Beta sets for Sonnet/Opus and dominant Haiku mimicry (structured-outputs path) unchanged.

`capture-http-comprehensive.sh` (3×3×2): Sonnet and Opus stable (1 unique beta each). Haiku **bimodal** (structured-outputs vs claude-code+extended-cache-ttl) — same gray split as 2.1.154; PR keeps dominant structured-outputs set in `FullClaudeCodeHaikuMimicryBetas()` and documents the WARN in evidence only.

## Delta

### MODIFIED

- `DefaultClaudeCodeUserAgentVersion` / `CLICurrentVersion` / mimic + canonical observed UA: **2.1.154 → 2.1.156**.
- `anthropic-http-mimicry-baselines.json` `cc_version` field.
- Smoke default UA, sentinel rationale, capture baseline test.

### NOT changed

- TLS ja3 / `tk_canonical_cc_oauth` cipher profile bodies.
- `FullClaudeCodeMimicryBetas()` / `FullClaudeCodeHaikuMimicryBetas()` token lists.

## Scenarios

### Core positive

1. Given cc 2.1.156 bundle, when `capture_cc_fingerprint.py check --bundle`, then all critical HTTP+TLS fields match.
2. Given OAuth forward on canonical path, when UA is built, then wire version is 2.1.156.

### Core negative

1. Given stale 2.1.154 constants, when check runs against 2.1.156 capture, then UA mismatches fail the gate.

### Regression

- `TestFullClaudeCodeMimicryBetas_MatchesCC0156*` pass.
- TLS `check-tls` on capture bundle: OK.

## Validation

```bash
go test -tags=unit ./internal/pkg/claude/... -run TestFullClaudeCode
python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic
bash ops/anthropic/capture-cc-fingerprint.sh capture --http
TOKENKEY_CC_CAPTURE_HAIKU_N=5 TOKENKEY_CC_CAPTURE_SONNET_N=5 TOKENKEY_CC_CAPTURE_OPUS_N=5 \
  bash ops/anthropic/capture-http-comprehensive.sh
./scripts/preflight.sh
```

Evidence: `.tls_list/20260529T030652Z-cc-capture.bundle.json`, `.tls_list/20260529T030719Z-http-multi.log` (comprehensive; haiku WARN bimodal).
