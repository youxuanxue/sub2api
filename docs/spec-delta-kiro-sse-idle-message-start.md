# Spec delta: Kiro SSE idle drop (~90s)

## Background

Long Kiro thinking stalls on native OAuth accounts died at ~90.8s with ~225B
responses. Claude Code treats ping-only Anthropic SSE as idle; Kiro historically
deferred `message_start` until visible text/tool. Caddy `encode` buffering of
`text/event-stream` was a compounding factor and is owned by
`fix(ops): 禁止 Caddy gzip 缓冲 SSE keepalive` (#2598) +
`deploy/caddy-encode-ssot.sh`.

## Delta

### ADDED

- Kiro `forwardStreaming` owns Claude-Code-safe pre-content keepalive:
  `message_start` before the first Anthropic `ping` (shared helper with
  Responses→Anthropic). Applies to `/v1/messages` and CC→Kiro.
- `defer stopPreContentStreamKeepalive` so early failover paths cannot leak the
  keepalive goroutine.

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

- Unit: Kiro keepalive / message_start-before-ping / ensure-from-config stall /
  sole-owner / failover-empty.
