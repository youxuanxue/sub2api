# spec-delta: cc 2.1.152 HTTP mitm beta alignment (Sonnet)

## Background

HTTP mitm capture (`capture --http` via `http_capture_invoke.sh`) on cc 2.1.152
shows the Sonnet OAuth mimic `anthropic-beta` set no longer carries `effort`.
Every capture taken (06:30Z pre-#427 chain, plus 11 captures on the merged #427
chain) agrees on the Sonnet token set, so the drop is stable and unimodal.

## Delta

- MODIFIED: `FullClaudeCodeMimicryBetas()` / `DefaultBetaHeader` — Sonnet without `effort`

## Haiku deliberately NOT changed — it is bimodal (scope cut)

An earlier draft also rewrote the Haiku mimic set (drop `claude-code` +
`extended-cache-ttl`, add `structured-outputs`) from a single capture. That was
dropped because cc 2.1.152 Haiku emits **two different beta sets that alternate**
across captures on the same canonical chain:

- **Variant A (8 tokens)**: `oauth, interleaved-thinking, context-management,
  prompt-caching-scope, claude-code-20250219, advisor-tool,
  extended-cache-ttl-2025-04-11, cache-diagnosis` — the current repo baseline.
- **Variant B (7 tokens)**: `oauth, interleaved-thinking, context-management,
  prompt-caching-scope, advisor-tool, structured-outputs-2025-12-15,
  cache-diagnosis` — what #428 originally aligned to.

Measured distribution over 11 post-#427 captures (same prompt, same model,
`http_capture_invoke.sh`): **A ≈ 7, B ≈ 4** (~60/40). Both are real, current cc
traffic. Sonnet never varied across the same runs.

A single `HaikuBetaHeader` constant cannot match a bimodal distribution: picking
A mismatches ~40% of real Haiku traffic, picking B mismatches ~60%. Swapping
main A → #428 B does not reduce drift, it only changes which half mismatches.

**Likely root cause (to confirm before any Haiku alignment):** cc fires more
than one Haiku request per session (e.g. the main response plus a background
task such as title generation), each with its own beta set, and the capture
records one per run (last-wins by model). If so, "the canonical Haiku beta set"
is ill-defined until the capture tool records *all* Haiku requests per session
instead of last-wins — that tooling gap should be closed first.

## Scenarios

- **正向**: `capture --http` → `check --bundle` reports `match` on `betas.sonnet_mimicry`
- **负向**: Sonnet mimic path must not inject `effort-2025-11-24`
- **回归**: `TestFullClaudeCodeMimicryBetas_MatchesCC0152SonnetCapture` passes; Haiku tests unchanged from baseline

## Validation

```bash
go test -tags=unit ./internal/pkg/claude/... -run TestFullClaudeCode
go test -tags=unit ./internal/service/... -run 'TestGatewayService_getBetaHeader|TestFullClaudeCode'
python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic
bash ops/anthropic/capture-cc-fingerprint.sh capture --http
python3 ops/anthropic/capture_cc_fingerprint.py check --bundle .tls_list/*-cc-capture.bundle.json
```

Evidence bundles (local, gitignored): Sonnet drop confirmed on every capture;
Haiku variant A in `20260527T085040Z` / `20260527T085322Z`, variant B in
`20260527T063010Z` / `20260527T090751Z` (and alternating thereafter).
