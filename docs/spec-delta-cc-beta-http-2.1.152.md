# spec-delta: cc 2.1.152 HTTP mitm beta alignment (Sonnet)

## Background

HTTP mitm capture (`capture --http` via `http_capture_invoke.sh`) on cc 2.1.152
shows the Sonnet OAuth mimic `anthropic-beta` set no longer carries `effort`.
Two independent captures (06:30Z pre-#427 chain, and 08:50Z/08:53Z on the
merged #427 chain) agree on the Sonnet token set, so the drop is stable.

## Delta

- MODIFIED: `FullClaudeCodeMimicryBetas()` / `DefaultBetaHeader` — Sonnet without `effort`

## Haiku deliberately NOT changed (scope cut)

An earlier draft of this work also rewrote the Haiku mimic set (drop
`claude-code` + `extended-cache-ttl`, add `structured-outputs`) based on a
single pre-#427 capture (`20260527T063010Z`). That change was dropped because
it is **not reproducible** on the canonical post-#427 capture chain:

- `20260527T085040Z` and `20260527T085322Z` (two consecutive captures on the
  merged #427 HTTP chain) both return the **8-token** Haiku set
  (`oauth, interleaved-thinking, context-management, prompt-caching-scope,
  claude-code-20250219, advisor-tool, extended-cache-ttl-2025-04-11,
  cache-diagnosis`) — identical to the current repo baseline.
- The `structured-outputs` 7-token Haiku set appears **only** in the single
  pre-#427 `20260527T063010Z` bundle.

Changing Haiku to the non-reproducible set would drift mimicry away from
observed real traffic. Re-investigate before touching Haiku: whether the
pre-#427 chain invoked Haiku via a different request path, or whether cc emits
request-dependent Haiku beta sets.

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

Evidence bundles (local, gitignored): `20260527T063010Z` (pre-#427, Haiku divergent),
`20260527T085040Z` / `20260527T085322Z` (post-#427, Sonnet drop confirmed, Haiku stable).
