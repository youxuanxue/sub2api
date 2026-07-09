# spec-delta: cc 2.1.153 OAuth mimicry alignment

## Background

cc 2.1.153 mitm + TLS capture (2026-05-28, cc0 → gost → socks) shows HTTP fingerprint drift from PR #423 baseline (2.1.152). TLS ClientHello is **unchanged** (same ja3_hash); no DB TLS profile migration.

Prior `docs/spec-delta/cc-beta-http-2.1.152.md` documented bimodal Haiku beta on 2.1.152. cc 2.1.153 Haiku capture is **bimodal** on two 9-token sets (both include `thinking-token-count`); PR targets dominant **structured-outputs** variant B (~73% in comprehensive runs).

## Delta

### ADDED

- `BetaThinkingTokenCount`, `BetaStructuredOutputs` in `constants.go`.
- `capture_cc_fingerprint._pick_http_by_model`: **last-wins** per variant (was first-wins) so comprehensive → bundle `check` uses the final haiku/sonnet record, not the opening one.

### MODIFIED

- `DefaultClaudeCodeUserAgentVersion` / `CLICurrentVersion` / mimic UA: **2.1.152 → 2.1.153**.
- `FullClaudeCodeMimicryBetas()`: insert `thinking-token-count-2026-05-13` after `interleaved-thinking`.
- `FullClaudeCodeHaikuMimicryBetas()`: drop `claude-code` + `extended-cache-ttl`; add `thinking-token-count` + `structured-outputs`.
- `DefaultBetaHeader`, `HaikuBetaHeader`, smoke default UA, sentinel anchors.

### NOT changed

- TLS ja3 / `tk_canonical_cc_oauth` cipher profile bodies.
- `gateway_service.go` mimic call sites (already delegate to `FullClaudeCode*MimicryBetas()`).

## Scenarios

### Core positive

1. Given cc 2.1.153 bundle, when `capture_cc_fingerprint.py check --bundle`, then all critical HTTP+TLS fields match.
2. Given OAuth Haiku forward, when gateway merges mimic betas, then 8-token 2.1.153 set is used (no `claude-code` / `extended-cache-ttl`).

### Core negative

1. Given stale 2.1.152 constants, when check runs against 2.1.153 capture, then UA + beta mismatches fail the gate.

### Regression

- `TestFullClaudeCodeMimicryBetas_MatchesCC0153SonnetCapture` / Haiku counterpart pass.
- `capture-http-comprehensive.sh` reports OK (single unique beta per model family) across 5 runs post-merge.

## Validation

```bash
go test -tags=unit ./internal/pkg/claude/... -run TestFullClaudeCode
python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic
bash ops/anthropic/capture-cc-fingerprint.sh capture --http
bash ops/anthropic/capture-http-comprehensive.sh   # ×5 for beta consistency
./scripts/preflight.sh
```

Evidence: `.tls_list/20260528T090600Z-cc-capture.bundle.json` (initial single HTTP capture).
