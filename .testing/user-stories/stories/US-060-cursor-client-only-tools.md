# US-060 Cursor Agent client-only tool execution

- ID: US-060
- Priority: P1
- Title: Bridge native tools to the caller without gateway execution
- As a / I want / So that: As an API caller, I want Agent tool calls returned as standard client tools so that my own permissions and workspace own execution.
- Trace: docs/approved/design-cursor-tool-gateway-compat.md; explicit conversation approval of Agent mode and prohibition on gateway execution.
- Risk Focus:
  - 逻辑错误：argument conversion, schema constraints and client result continuation.
  - 行为回归：Messages/Chat/Responses streaming, usage and terminal events.
  - 安全问题：local execution fallback, undeclared tools, sandbox bypass and schema URL access.
  - 运行时问题：cancellation, upstream idle timeout and bounded retained upstream handoff.

## Acceptance Criteria

1. AC-001 (positive): Given declared compatible Read/Write/Bash/Grep tools When native exec arrives Then emit standard tool calls and preserve arguments; client results and errors reach the original upstream exec on the same connection.
2. AC-002 (negative): Given missing or incompatible declarations, unsupported native semantics or external schema references When converting Then reject without local execution or schema fetch.
3. AC-003 (security): Given gateway files and a shell marker target When native Read/Write/Bash/Grep arrive Then file contents are not read into output or modified and the marker is not created.
4. AC-004 (regression): Given each public protocol and streaming mode When native handoff occurs Then model identity and billing provenance remain correct; rejected tools permit further text.
5. AC-005 (runtime): Given cancellation, expiry or concurrent continuation When waiting for a client result Then the upstream is released and results cannot cross authenticated callers or be consumed twice.
6. AC-006 (result fidelity): Given client tool errors or partial output When returning native results Then preserve original errors without invented refusal/exit classifications, omit whole-file metadata for a Read page, and preserve Grep match content without overflowing totals. Tool-result requests with trailing user input fail closed instead of starting another upstream run.

## Assertions

Compare complete translated arguments and returned content. A new unpredictable client result must reach the original exec before the fixture permits Write and completion. Intermediate segments settle zero usage; terminal usage is not charged twice. Check actual file contents, absent shell marker and zero schema HTTP requests. Pin forbidden local executor imports in addition to behavior tests.

## Linked Tests

- `backend/internal/integration/cursor/native_client_tools_tk_test.go`::`TestNativeClientToolArguments`
- `backend/internal/integration/cursor/native_client_tools_tk_test.go`::`TestNativeClientToolsFailClosed`
- `backend/internal/integration/cursor/native_runs_tk_test.go`::`TestNativeToolsRetainedRoundTrip`
- `backend/internal/integration/cursor/native_runs_tk_test.go`::`TestNativeRunIsolationExpiryAndCancellation`
- `backend/internal/integration/cursor/native_runs_tk_test.go`::`TestNativeClientResultSemantics`
- `backend/internal/integration/cursor/native_boundary_tk_test.go`::`TestNativeClientResultEvidence`
- `backend/internal/integration/cursor/native_boundary_tk_test.go`::`TestNativeShellErrorContinuesOriginalRun`
- `backend/internal/integration/cursor/native_boundary_tk_test.go`::`TestNativeContinuationTrailingTextFailsClosed`
- `backend/internal/integration/cursor/native_boundary_tk_test.go`::`TestNativeContinuationConcurrentSingleConsumption`
- `backend/internal/integration/cursor/native_client_tools_tk_test.go`::`TestCursorAdapterCannotImportLocalExecutors`
- `backend/internal/integration/cursor/messages_agentrun_tk_test.go`::`TestAgentRunOutsideExecThrowsAndContinuesText`
- `backend/internal/service/cursor_native_transport_regression_test.go`::`TestCursorProtocolRoutesUseNativeTransportAndSettlement`

- Run command: `cd backend && go test -race ./internal/integration/cursor`
- Run command: `cd backend && go test -tags=unit ./internal/service -run 'TestCursor|TestOpenAI.*Transport|TestMappedResponseModel' -count=1`

Commands:

```sh
cd backend
go test -race ./internal/integration/cursor
go test -tags=unit ./internal/service -run 'TestCursor|TestOpenAI.*Transport|TestMappedResponseModel' -count=1
```

## Evidence

Retained duplex fixtures pass for Read → Write → Bash → Grep, including missing-file creation and existing-file content. Runtime tests cover owner/key/schema mismatch, duplicates, limits, expiry and cancellation. Gateway files and shell markers are checked for side effects. This replaces the failed cancel/replay prototype. On 2026-10-04, isolated probes on account 150's prod host passed buffered and SSE Read/Write/Bash, including Write's preparatory missing-file Read and a bounded 30-second Bash handoff. Client results were simulated; gateway tool targets remained absent. Grep was rejected by upstream policy before tool emission and was not retried, so live Grep acceptance remains open. No actual user 16 traffic was replayed. The active image and account scheduling were unchanged. Evidence: `.cache/observability/cursor150-retained-20261004/report.md`. These are protocol integration tests, not UI e2e. No deployment is part of this change.

## Status

- InTest
