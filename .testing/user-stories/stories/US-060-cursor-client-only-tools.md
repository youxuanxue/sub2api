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

1. AC-001 (positive): Given declared compatible Read/Write/Bash/Grep tools When compatible native Read/Write/Grep or declared MCP Bash arrives Then emit standard tool calls and preserve arguments; client results and errors reach the original upstream exec on the same connection.
2. AC-002 (negative): Given missing or incompatible declarations, unsupported native semantics or external schema references When converting Then reject without local execution or schema fetch.
3. AC-003 (security): Given gateway files and a shell marker target When native Read/Write/Grep or declared MCP Bash arrives Then file contents are not read into output or modified and the marker is not created.
4. AC-004 (regression): Given each public protocol and streaming mode When native handoff occurs Then model identity and billing provenance remain correct; rejected non-Shell native tools permit further text. Native Shell ends with a neutral compatibility error and no automatic failover.
5. AC-005 (runtime): Given cancellation, expiry or concurrent continuation When waiting for a client result Then the upstream is released and results cannot cross authenticated callers or be consumed twice.
6. AC-006 (result fidelity): Given client tool errors or partial output When returning native results Then preserve original errors without gateway-invented refusal/exit classifications, omit whole-file metadata for a Read page, and preserve Grep match content without overflowing totals. Tool-result requests with trailing user input fail closed instead of starting another upstream run; intervening system/developer messages cannot hide pending affinity. Native non-Shell error rendering is outside this guarantee. Bash uses MCP text/is_error without native execution metadata.
7. AC-007 (Shell protocol boundary): Given a declared Bash tool When routed through MCP Then preserve schema, permissions, command and client result; if native Shell is selected, reject before client handoff. No gateway execution, environment, command rewriting, fabricated exit metadata or new upstream replay is allowed.
8. AC-008 (discovery): Given client declarations When upstream requests MCP state with either kick-only value Then return only the requested current declaration snapshot and continue to client handoff on the original connection; unknown namespaces return no tools. No server is started or contacted.
9. AC-009 (schema fidelity): Given unchanged client Bash arguments When the caller schema permits additional properties or local references Then preserve and hand off the complete valid arguments; invalid arguments and external schema references fail without network access.
10. AC-010 (fixed tool filter): Given declared client tools When opening the upstream Run Then allow only MCP invocation and discovery, without modifying the system prompt; upstream errors cannot expand this list or trigger a replay. No tools and tool_choice=none leave the filter disabled.

## Assertions

Compare complete translated arguments and returned content. A new unpredictable client result must reach the original exec before the fixture permits Write and completion. Intermediate segments settle zero usage; terminal usage is not charged twice. Check actual file contents, absent shell marker and zero schema HTTP requests. Pin forbidden local executor imports in addition to behavior tests.

## Linked Tests

- `backend/internal/integration/cursor/native_client_tools_tk_test.go`::`TestNativeClientToolArguments`
- `backend/internal/integration/cursor/native_client_tools_tk_test.go`::`TestNativeClientToolsFailClosed`
- `backend/internal/integration/cursor/native_runs_tk_test.go`::`TestNativeToolsRetainedRoundTrip`
- `backend/internal/integration/cursor/native_runs_tk_test.go`::`TestNativeRunIsolationExpiryAndCancellation`
- `backend/internal/integration/cursor/native_runs_tk_test.go`::`TestNativeClientResultSemantics`
- `backend/internal/integration/cursor/native_boundary_tk_test.go`::`TestNativeClientResultEvidence`
- `backend/internal/integration/cursor/native_boundary_tk_test.go`::`TestClientBashErrorContinuesOriginalRun`
- `backend/internal/integration/cursor/native_boundary_tk_test.go`::`TestNativeContinuationTrailingTextFailsClosed`
- `backend/internal/integration/cursor/native_boundary_tk_test.go`::`TestNativeContinuationConcurrentSingleConsumption`
- `backend/internal/integration/cursor/native_client_tools_tk_test.go`::`TestCursorAdapterCannotImportLocalExecutors`
- `backend/internal/integration/cursor/messages_agentrun_tk_test.go`::`TestAgentRunOutsideExecThrowsAndContinuesText`
- `backend/internal/service/cursor_native_transport_regression_test.go`::`TestCursorProtocolRoutesUseNativeTransportAndSettlement`

- `backend/internal/integration/cursor/client_shell_routing_tk_test.go`::`TestClientToolRoutingPreservesDeclarations`
- `backend/internal/integration/cursor/client_shell_routing_tk_test.go`::`TestClientBashRejectsInvalidArguments`
- `backend/internal/integration/cursor/client_shell_routing_tk_test.go`::`TestNativeShellFailsBeforeClientHandoff`
- `backend/internal/integration/cursor/client_shell_routing_tk_test.go`::`TestClientBashPreservesCallerSchemaSemantics`
- `backend/internal/integration/cursor/client_tool_metadata_tk_test.go`::`TestClientToolMetadataSnapshot`
- `backend/internal/integration/cursor/client_tool_metadata_tk_test.go`::`TestClientToolMetadataContinuesToClientHandoff`
- `backend/internal/integration/cursor/agent_test.go`::`TestAgentClientToolAllowlistIncludesDiscovery`
- `backend/internal/integration/cursor/client_shell_routing_tk_test.go`::`TestClientToolFilterDisabledWithoutDeclarations`
- `backend/internal/integration/cursor/client_shell_routing_tk_test.go`::`TestClientToolAllowlistDoesNotLearnOrReplay`

- Run command: `cd backend && go test -race ./internal/integration/cursor`
- Run command: `cd backend && go test -tags=unit ./internal/service -run 'TestCursor|TestOpenAI.*Transport|TestMappedResponseModel' -count=1`

Commands:

```sh
cd backend
go test -race ./internal/integration/cursor
go test -tags=unit ./internal/service -run 'TestCursor|TestOpenAI.*Transport|TestMappedResponseModel' -count=1
```


## Evidence

Retained duplex fixtures cover native Read → Write → MCP Bash → native Grep, including missing-file creation and existing-file content. Runtime tests cover owner/key/schema mismatch, duplicates, limits, expiry and cancellation. Gateway files and shell markers are checked for side effects. Error continuation fixtures require the exact original exec ID, error text and code; success paths reject unexpected throws.

On 2026-10-06, isolated probes of PR #2447 at `e6e36ba27` on account 150's prod host passed buffered and SSE Read/Write/Bash/Grep using `composer-2.5`. Grep passed with complete declarations, including explicit false `-i` and `multiline`; a narrower schema correctly rejected it. Undeclared native tools were rejected within the model stream; trailing user input and duplicate results failed without starting another upstream connection. Client results were simulated and gateway tool targets remained absent.

Shell error text reached the original exec, but the upstream wrapped it as `Command failed to spawn`; classification fidelity is not proven. The model still attempted to reuse a Shell variable across calls; truthful simulated client feedback let it report the failure. TokenKey must not supply an environment to make that assumption work. Evidence: `.cache/observability/cursor150-pr2447-live/report.md`.

These are protocol integration tests, not UI e2e or production user 16 acceptance. They did not exercise production scheduling, a real client executor or billing persistence. At that snapshot production was still on 1.8.272, account 150 remained unschedulable and its previous Claude model mapping was absent. Deployment and scheduling changes are outside this PR.

The client Shell routing revision supersedes the native Shell result adapter above.
Prod-host candidate tests passed buffered/SSE success, tool error and successive
independent calls. All client results used MCP on the original connection;
upstream tool results contained no invented persistence, duration or spawn-error
classification. Results were simulated; no gateway tool executed. Evidence:
`.cache/observability/cursor150-mcp-routing/report.md`. Candidate tests do not
establish deployed production recovery; #150 scheduling remains operator-controlled.

Subsequent review found unsupported MCP state queries and overly restrictive
validation of unchanged Bash arguments. Static declaration replies and direct
JSON Schema validation now have buffered/SSE duplex and schema-fidelity coverage.
Official CLI exclusion and bounded native-rejection recovery were probed on the
prod host but did not reliably reach client handoff; neither prototype is shipped.
Evidence: `.cache/observability/cursor150-metadata-routing/`. A native Shell choice
still terminates this request, so universal compatibility remains unproven.

A subsequent prod-host A/B isolated the complete MCP allowlist without any routing
hint. The unfiltered request selected native Shell and ended with 422; permitting
both MCP invocation and discovery instead returned client Bash and completed on
the same upstream connection. The implementation now uses that fixed filter for
declared tools and removes the routing prompt. Native exec guards remain active.
Evidence: `.cache/observability/cursor150-mcp-allowlist/`.

The final candidate passed all twelve isolated prod-host scenarios: buffered/SSE
Bash success, client error, successive calls, Read, missing-file Write and Grep.
Every case used AGENT mode, static metadata queries and MCP calls on one original
connection; no native workspace exec or gateway tool execution occurred. Read
continuations also rejected duplicate results and trailing user input without
replay. Both successive-call cases correctly described the empty variable in a
fresh client process. Three generated replies claimed an automatic model switch;
this is not verified model identity or billing evidence. Account 150 remained
unschedulable, and this candidate was not deployed. SSM evidence command:
`fd4e7370-f315-48be-9b45-4bf3cd2cc271` (2026-10-07).

## Status

- InTest
