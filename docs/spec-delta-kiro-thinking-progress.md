# Spec delta: Kiro reasoning must not trip the first-output silence budget

## Background

`claude-opus-5-5` on Kiro with thinking/adaptive uses the universal high-effort
first-useful-output budget (default 60s, cap 60). Commit is client-visible text
or a tool. Unsigned `reasoningContentEvent` frames do not Commit, so a live
Opus thinking stream was failed over at ~60s with no assistant body. The client
then retried `stream:false`. Non-streaming Kiro writes JSON only after the
EventStream completes, so those retries sat until the caller disconnected
(~6 min) and also produced no body and no usage row.

SSE keepalive (`message_start` + ping) is unchanged and is still not useful
output. A silent upstream (no frames) still uses the 30s/60s budget.

## Delta

### ADDED

- While Kiro emits reasoning / unsigned thinking frames, the silence timer
  resets. Wall clock for that reset is capped at 15 minutes
  (`kiroThinkingProgressCap`).
- After the cap, or after a full silence window with no further frames, the
  attempt is still `first_output_unavailable` and may failover. Nothing was
  committed, so replay stays legal.

### MODIFIED

- None of the universal 30s/60s defaults, the 60s config cap, or the rule that
  thinking does not Commit.

### REMOVED

- None.

## Scenarios

| Kind | Scenario |
| --- | --- |
| Positive | Reasoning frames continue past the silence budget, then visible text arrives before the 15m cap. Stream completes and returns `ForwardResult` (billable). Thinking text stays off the client wire. |
| Negative | One reasoning frame, then silence longer than the budget. Failover `first_output_unavailable`, no `message_stop`, no usage result. |
| Regression | No frames at all still failover at the configured silence budget. `NoteUpstreamProgress` does nothing unless Kiro enabled the extension. |

## Validation

```bash
cd backend
go test -tags=unit ./internal/service/ -count=1 -timeout 180s -run 'TestKiroStreaming_Reasoning|TestStreamFirstOutputGuardProgress|TestKiroStreaming_FirstOutputTimeoutAfterKeepalive|TestStreamFirstOutputGuardBudgetFired|TestStreamFirstOutputGuardCommitDisarms'
```
