# spec-delta: cc 2.1.154 UA alignment

## Background

cc 2.1.154 mitm + TLS capture (2026-05-29, cc0 → gost → socks) shows **UA-only** drift from 2.1.153. TLS ClientHello unchanged (same ja3_hash). Beta sets for Sonnet/Opus and dominant Haiku mimicry unchanged.

`capture-http-comprehensive.sh` with 5 requests per model family: Sonnet and Opus stable (1 unique beta each). Haiku **bimodal** (structured-outputs vs claude-code+extended-cache-ttl) — same as 2.1.153; PR keeps dominant structured-outputs set in `FullClaudeCodeHaikuMimicryBetas()`.

## Delta

### MODIFIED

- `DefaultClaudeCodeUserAgentVersion` / `CLICurrentVersion` / mimic + canonical observed UA: **2.1.153 → 2.1.154**.
- `anthropic-http-mimicry-baselines.json` `cc_version` field.
- Smoke default UA, sentinel rationale, capture baseline test.

### NOT changed

- TLS ja3 / `tk_canonical_cc_oauth` cipher profile bodies.
- `FullClaudeCodeMimicryBetas()` / `FullClaudeCodeHaikuMimicryBetas()` token lists.

## Scenarios

### Core positive

1. Given cc 2.1.154 bundle, when `capture_cc_fingerprint.py check --bundle`, then all critical HTTP+TLS fields match.
2. Given OAuth forward on canonical path, when UA is built, then wire version is 2.1.154.

### Core negative

1. Given stale 2.1.153 constants, when check runs against 2.1.154 capture, then UA mismatches fail the gate.

### Regression

- `TestFullClaudeCodeMimicryBetas_MatchesCC0154*` pass.
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

Evidence: `.tls_list/20260528T235304Z-cc-capture.bundle.json`, `.tls_list/20260528T235326Z-http-multi.log` (5×5×5 comprehensive).
