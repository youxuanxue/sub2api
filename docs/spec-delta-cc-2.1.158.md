# spec-delta: cc 2.1.158 UA alignment

## Background

cc 2.1.158 mitm + TLS capture (2026-05-30, cc0 -> gost -> socks) shows **UA-only** drift from 2.1.157. TLS ClientHello is unchanged (`ja3_hash=d871d02cecbde59abbf8f4806134addf`). Stainless package version remains `0.94.0`.

`capture-http-comprehensive.sh` (3x3x2) showed Sonnet and Opus stable at one beta header each. Haiku remains bimodal: the structured-outputs path is still the captured baseline, while a minority path adds `claude-code-20250219` and `extended-cache-ttl-2025-04-11` and drops `structured-outputs-2025-12-15`. Because haiku is split, this PR does not change beta constants.

## Delta

### MODIFIED

- `DefaultClaudeCodeUserAgentVersion` / `CLICurrentVersion` / mimic + canonical observed UA: **2.1.157 -> 2.1.158**.
- `anthropic-http-mimicry-baselines.json` `cc_version` field.
- Smoke default UA, sentinel rationale, capture baseline test, and comments that name the latest cc capture.
- `capture-cc-fingerprint.sh check env` no-arg invocation now works on macOS Bash 3.2 with `set -u`.

### NOT changed

- TLS ja3 / `tk_canonical_cc_oauth` cipher profile bodies.
- `FullClaudeCodeMimicryBetas()` / `FullClaudeCodeHaikuMimicryBetas()` token lists.
- Stainless package/runtime fields.

## Scenarios

### Core positive

1. Given cc 2.1.158 bundle, when `capture_cc_fingerprint.py check --bundle` runs against repo baseline, then all critical HTTP+TLS fields match.
2. Given OAuth forward on canonical path, when UA is built, then wire version is 2.1.158.

### Core negative

1. Given stale 2.1.157 constants, when check runs against 2.1.158 capture, then UA mismatches fail the gate.

### Regression

- `TestFullClaudeCodeMimicryBetas_MatchesCC0158*` pass.
- TLS `check-tls` on capture bundle: OK.

## Validation

```bash
bash ops/anthropic/capture-cc-fingerprint.sh capture --http
bash ops/anthropic/capture-http-comprehensive.sh
python3 ops/anthropic/capture_cc_fingerprint.py check --bundle /Users/xuejiao/Codes/token/tk/sub2api/.tls_list/20260530T062407Z-cc-capture.bundle.json
bash ops/anthropic/capture-cc-fingerprint.sh check-tls --bundle /Users/xuejiao/Codes/token/tk/sub2api/.tls_list/20260530T062407Z-cc-capture.bundle.json
go test -tags=unit ./internal/pkg/claude/... -run TestFullClaudeCode
python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic
./scripts/preflight.sh
```

Post-merge runtime sync (no release required for HTTP UA):

```bash
bash ops/anthropic/cc_fingerprint_apply_http_runtime.sh
```

Evidence: `.tls_list/20260530T062407Z-cc-capture.bundle.json`, `.tls_list/20260530T062431Z-http-multi.log` (comprehensive; haiku WARN bimodal).
