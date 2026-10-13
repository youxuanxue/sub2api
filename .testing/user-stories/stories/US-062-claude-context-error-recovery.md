# US-062 Claude context error recovery

- ID: US-062
- Priority: P1
- Title: Make context-limit errors recoverable and failures observable
- As a / I want / So that: As a Claude Code user, I want context errors to reach the client correctly so that it can compact or report a real failure.
- Trace: docs/approved/claude-context-error-recovery.md; user16 production evidence and approved conversation scope.
- Risk Focus:
  - 逻辑错误：precise context-error recognition without inventing token counts.
  - 行为回归：HTTP JSON vs committed SSE; recovered success remains successful.
  - 安全问题：near-match/quoted errors must not trigger context recovery; no history mutation or retry expansion.
  - 运行时问题：upstream rejection after heartbeat must terminate visibly with truthful Ops attribution.

## Acceptance Criteria

1. AC-001 (positive): Given an exact supplier context rejection When forwarding before or after a heartbeat Then emit a recognizable Anthropic error, preserve original evidence and make no unchanged upstream retry.
2. AC-002 (negative): Given another error, status or an embedded quoted phrase When writing Then preserve the original error and status.
3. AC-003 (regression): Given a late rejection or a genuinely successful retry When Ops records the request Then late rejection is a logical failure and successful retry alone remains Recovered.
4. AC-004 (runtime): Given Claude Code 2.1.283 and controlled upstream responses When automatic recovery and manual compact run Then verify client error recognition, recovery attempts and actual successful replies, recording any limitations.

## Assertions

Assert complete JSON/SSE framing, error type/message, status, unchanged upstream call count, original error evidence and persisted Ops result. Real CLI acceptance uses synthetic conversation only and loopback upstream; no customer payload or production credentials.

## Linked Tests

- `backend/internal/service/gateway_anthropic_error_recovery_tk_test.go`::`TestUS062_AnthropicContextErrorRecovery`
- `backend/internal/service/gateway_anthropic_error_recovery_tk_test.go`::`TestUS062_AnthropicContextErrorMatchBoundaries`
- `backend/internal/service/gateway_anthropic_error_recovery_tk_test.go`::`TestUS062_AnthropicNativeErrorFraming`
- `backend/internal/handler/ops_error_logger_anthropic_context_tk_test.go`::`TestUS062_AnthropicContextErrorOutcome`
- `scripts/checks/claude-context-error-cli.py` — AC-004; opt-in real client with `TestClaudeContextCLIFixture` loopback gateway.

Run: `cd backend && go test -tags=unit ./internal/service ./internal/handler -run 'TestUS062_' -count=1`

## Evidence

2026-10-13, real `2.1.283 (Claude Code)` against synthetic loopback upstream:

| Scenario | Observed recovery | Final continuation |
| --- | --- | --- |
| Automatic | 400 → summary succeeds | 56,835 → 1,883 bytes; 9 → 1 messages; `SYNTHETIC_OK` |
| After heartbeat | SSE error → client nonstream fallback also rejects → summary succeeds | 56,835 → 1,883 bytes; 9 → 1 messages; `SYNTHETIC_OK` |
| Manual `/compact` | Summary rejects → smaller summary succeeds → continuation in the same process | 63,237 → 2,646 bytes; 9 → 1 messages; `SYNTHETIC_OK` |

Reproduce: `python3 scripts/checks/claude-context-error-cli.py --claude /path/to/claude-2.1.283`
The driver uses fresh isolated config/sessions, synthetic credentials and no tools. It checks successful results, recovery summaries, and reduced continuation bytes **and** message count against seeded history. Client-generated retries are observed; the gateway introduces none. The manual scenario does not claim cross-process compact persistence.

Full `go test -tags=unit ./internal/service ./internal/handler` passed, as did `go test -race -tags=unit ./internal/service ./internal/handler -run 'TestUS062_' -count=1`. The tests verify JSON/SSE boundaries, unrelated native error fields, exact-match negatives, original upstream diagnostics, logical failure attribution and genuine recovered success.

Production waiting/recovery timing is not reproduced: supplier internal state is unproven. No deployment claim.

## Status

- Done
