# Spec delta: Kiro SSE idle drop (~90s)

## Background

Long Kiro thinking stalls on thinking-relay edges died at ~90.8s with ~225B
responses. Claude Code treats ping-only Anthropic SSE as idle; Kiro historically
deferred `message_start` until visible text/tool. Edge/prod Caddy `encode zstd
gzip` also buffered `text/event-stream`.

## Delta

### ADDED

- Kiro `forwardStreaming` owns Claude-Code-safe pre-content keepalive:
  `message_start` before the first Anthropic `ping` (shared helper with
  Responses→Anthropic). Applies to `/v1/messages` and CC→Kiro.
- Stage0 Caddy encode allowlists that exclude `text/event-stream`, gated by
  `scripts/checks/caddyfile-sse-encode.sh`.

### MODIFIED

- Kiro Anthropic SSE may emit `message_start` at the first keepalive interval
  (default 10s) during thinking stalls, before visible content.
- After that frame, `c.Writer.Written()` / `enc.started` failover gate matches
  the existing Responses→Anthropic tradeoff.

### REMOVED

- Keepalive binding from `tkTryForwardKiro` (moved into `forwardStreaming` SSOT).

## Scenarios

| Kind | Scenario |
| --- | --- |
| Positive | Thinking stall > keepalive interval → client sees `message_start` then `ping`, then visible text; stream completes. |
| Negative | Fast upstream 400 before one interval → no SSE bytes; typed failover/`KiroInvalidModelError` preserved. |
| Regression | Visible text still drives `first_token_ms`; unsigned thinking stays off the client wire. |

## Validation

- Unit: Kiro keepalive / message_start-before-ping / sole-owner / failover-empty.
- `caddyfile-sse-encode.sh`, `caddyfile-syntax.sh`, `build-cfn --check`,
  `render-bootstrap --check`.
