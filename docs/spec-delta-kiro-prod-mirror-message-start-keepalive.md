# Spec delta: prod→edge Kiro mirror message_start before ping

## Background

v1.8.287 live probe of prod `kiro-us5` (thinking-relay) completed the stream but
emitted `ping` before `message_start`. Edge-native Kiro (#2600) was already
correct; the residual was prod's Anthropic **in-stream** keepalive (and Kiro
mirror header-wait) writing ping-only while waiting for the first upstream SSE
byte during edge thinking stalls.

## Delta

### ADDED

- `anthropicStreamIdleKeepaliveSSE`: synthesize `message_start` once (if not yet
  sent) then emit the standard Anthropic `ping`.
- `beginAnthropicMessagesHeaderWaitKeepalive`: Kiro mirror stubs use
  message_start-before-ping on header wait.

### MODIFIED

- `handleStreamingResponse` in-stream keepalive uses the idle helper (noop-delta
  Claude Code path unchanged).
- Upstream `message_start` is skipped on the client wire when keepalive already
  synthesized one; usage merge still applies.
- Anthropic passthrough in-stream keepalive matches the same contract and skip.

### REMOVED

- None.

## Scenarios

| Kind | Scenario |
| --- | --- |
| Positive | Pre-content stall > keepalive interval → client sees `message_start` then `ping`, then upstream content; single `message_start`. |
| Negative | Upstream `message_start` within one interval → no synthetic frame; existing ping-after-content behavior unchanged. |
| Regression | Affected Claude Code noop-delta keepalive still uses empty content_block_delta, not message_start. |

## Validation

- Unit: `StreamingKeepaliveEmitsMessageStartBeforePingOnPreContentStall`,
  `BeginAnthropicMessagesHeaderWaitKeepalive_KiroMirrorUsesMessageStart`,
  existing idle/noop-delta keepalive tests.
- Live (post-deploy): prod `ACCOUNT_ID=71` stream thinking probe must report
  `message_start` before `ping`.
