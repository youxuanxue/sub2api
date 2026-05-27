# spec-delta: cc 2.1.152 HTTP mitm beta alignment

## Background

HTTP mitm capture (`capture --http` via `http_capture_invoke.sh`) on cc 2.1.152
diverged from PR #423 constants: Haiku dropped `claude-code` / `extended-cache-ttl`
and added `structured-outputs`; Sonnet dropped `effort`.

## Delta

- MODIFIED: `FullClaudeCodeHaikuMimicryBetas()` — 7 tokens per Haiku mitm capture
- MODIFIED: `FullClaudeCodeMimicryBetas()` / `DefaultBetaHeader` — Sonnet without `effort`
- ADDED: `BetaStructuredOutputs` constant

## Scenarios

- **正向**: `capture --http` → `check --bundle` reports `match` on `betas.*`
- **负向**: Haiku mimic path must not inject `claude-code-20250219` when merging OAuth mimicry
- **回归**: `TestFullClaudeCode*MimicryBetas_MatchesCC0152*` pass

## Validation

```bash
go test -tags=unit ./internal/pkg/claude/... -run TestFullClaudeCode
go test -tags=unit ./internal/service/... -run 'TestGatewayService_getBetaHeader|TestFullClaudeCode'
python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic
bash ops/anthropic/capture-cc-fingerprint.sh capture --http
python3 ops/anthropic/capture_cc_fingerprint.py check --bundle .tls_list/*-cc-capture.bundle.json
```

Evidence bundle: `.tls_list/20260527T063010Z-cc-capture.bundle.json` (local, gitignored).
