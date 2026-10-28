# US-072-kiro-auto-mode-transcript-trim

- ID: US-072
- Title: Gated auto-mode transcript trim on Kiro overflow
- Priority: P1
- As a / I want / So that:
  As a TokenKey operator enabling a Kiro account Extra flag, I want oversized
  Claude Code auto-mode classifier requests to drop only whole old tool-process
  (then meta) transcript records under a size budget, so that some overflow
  can recover without head/tail truncation or silent summarization.
- Trace: `docs/approved/kiro-auto-mode-transcript-trim.md` (status: shipped; #2933, #2934, #2939, #2941)
- Risk Focus:
  - 逻辑错误: trim runs only after local overflow; under-budget requests unchanged.
  - 行为回归: flag off / non-candidate / parse failure → existing US-065 public 400 overflow path.
  - 安全问题: user lines and latest action retained; no head/tail or summary rewrite.
  - 运行时: still zero upstream on residual overflow; no failover; diagnostics counts only.

## Acceptance Criteria

1. **AC-001 (positive / S1)**: Given Extra `kiro_auto_mode_transcript_trim=true`, a
   parseable auto-mode NDJSON or content-block transcript with removable old
   tool-process records, and a translated payload over 900 KiB, when Forward
   runs, then records are removed oldest-first until `ValidatePayloadSize` passes
   or nothing removable remains; user text and latest action survive.
2. **AC-002 (negative / S2)**: Given the flag on but unparseable transcript or
   missing latest action, when overflow occurs, then the request body is not
   rewritten and the public envelope follows US-065: 400 `invalid_request_error`
   whose message leads with `prompt is too long`.
3. **AC-003 (negative / S3)**: Given the flag off, or a non-auto-mode oversized
   user prose request, when Forward runs, then no transcript rewrite occurs
   (US-065 behavior).
4. **AC-004 (regression / S4)**: Given the flag on and a request that already
   fits after normal translation, when Forward runs, then the Claude request
   bytes used for translation are unchanged by the trim path (`already_fits` /
   trim not invoked).
5. **AC-005 (budget)**: Given multiple removable tool-process records, when the
   budget fits after the first deletion, then later tool-process records are
   kept (no eager full drain).

6. **AC-006 (compact)**: Given the flag on, `context_management`, `max_tokens > 64`,
   and a final user message made of multiple pure text blocks over 256 KiB, when
   translation exceeds 900 KiB, then each block is an independent turn before
   history trimming; the newest block stays current and ordinary requests are
   unchanged.

## Assertions

- Account Extra accepts only Kiro + boolean `true`.
- Diagnostics may include `auto_mode_transcript_trim` counts/skip_reason, never body text.
- US-065 overflow tests remain green with the flag unset.

## Linked Tests

- `backend/internal/integration/kiro/auto_mode_transcript_trim_tk_test.go`::`TestDetectClaudeAutoModeTranscriptCandidate`
- `backend/internal/integration/kiro/auto_mode_transcript_trim_tk_test.go`::`TestMaybeTrimClaudeAutoModeTranscript_RemovesOldToolProcessKeepsUserAndAction`
- `backend/internal/integration/kiro/auto_mode_transcript_trim_tk_test.go`::`TestTrimClaudeAutoModeTranscriptUntil_StopsWhenBudgetFits`
- `backend/internal/integration/kiro/auto_mode_transcript_trim_tk_test.go`::`TestTrimClaudeAutoModeTranscript_ContentBlocksShape`
- `backend/internal/integration/kiro/auto_mode_transcript_trim_tk_test.go`::`TestMaybeTrimClaudeAutoModeTranscript_ParseFailureLeavesRequestUntouched`
- `backend/internal/integration/kiro/auto_mode_transcript_trim_tk_test.go`::`TestTrimClaudeAutoModeTranscriptUntil_AlreadyFitsNoMutation`
- `backend/internal/integration/kiro/compact_message_split_tk_test.go`::`TestSplitClaudeCompactUserMessage_PreservesBlocksAsTurns`
- `backend/internal/integration/kiro/compact_message_split_tk_test.go`::`TestSplitClaudeCompactUserMessage_AllowsExistingHistoryTruncatorToFitWire`
- `backend/internal/service/kiro_file_tool_chunking_test.go`::`TestKiroAutoModeTranscriptTrimAccountOptIn`
- US-065 suite (flag unset regression): see `US-065-kiro-payload-overflow.md`

Run:

```bash
cd backend && go test -tags=unit ./internal/integration/kiro ./internal/service ./internal/handler \
  -run 'AutoModeTranscript|DetectClaudeAutoMode|US065|Oversized|MessagesNonFailover|KiroAutoModeTranscriptTrimAccountOptIn' -count=1
```

## Evidence

Shipped with the gateway from `#2933` (1.8.329) through `#2941` (public 400 envelope).
2026-10-28: user confirmed development is complete and the path is released.
Account Extra default stays unset; rewrite runs only when
`kiro_auto_mode_transcript_trim=true`.

## Status

- [x] Done — opt-in trim and compact block split are in the released gateway; flag default remains off.
