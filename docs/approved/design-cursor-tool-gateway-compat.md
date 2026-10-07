---
title: Provider-neutral Cursor tool gateway compatibility
status: approved
approved_by: "feng (conversation approval, 2026-10-01; Agent/client-only bridge and revised continuation design approved 2026-10-04; client Shell routing revision approved in conversation 2026-10-07; metadata/schema refinement and fixed MCP allowlist authorized by subsequent conversation approvals)"
created: 2026-10-01
---

# Provider-neutral Cursor tool gateway compatibility

## Contract

TokenKey supplies model inference and standard external tool handoff. It does
not execute workspace tools. Cursor is an internal supply implementation and
is absent from public error text, public capability claims, and public model
identity.

## Protocol behavior

| Upstream frame | Gateway behavior |
| --- | --- |
| Native Read/Write/Grep, supported Pi equivalents | Match a declared Read/Write/Grep tool, validate translated arguments against its schema, emit standard client handoff |
| Native Shell/ShellStream/MiniSwe/PiBash | Stop before client handoff with provider-neutral `gateway_tool_unavailable`; HTTP 422 before output, terminal error after streaming starts; no fallback/replay |
| Undeclared, incompatible or other non-Shell native exec | `ExecClientThrow + StreamClose`, public capability error, continue the stream |
| Declared MCP/function tool | Normalize identity and emit standard tool handoff; MCP-only runs retain history replay, native-capable runs return results on the original connection |
| Unknown MCP tool | Return a bounded provider-neutral protocol error; never execute or silently pass through |
| MCP state query | Return only the current request's declared tools in the `tokenkey` namespace; no server discovery, startup or environment access |
| Text/thinking/usage | Convert through the existing Messages/Chat/Responses owners |

Unbridgeable non-Shell native exec rejection is a frame-level response, not an HTTP request failure.
The server must continue reading the stream so the model can answer without the
workspace tool. If the model cannot proceed, the resulting limitation is
reported as an ordinary capability error.

## Agent mode and client execution boundary

AGENT is used in current and history turns. Mode controls upstream planning;
it does not authorize any gateway filesystem/process execution. The only native
bridge owner is `native_client_tools_tk.go`. Ambiguous exec frames are rejected before metadata, context or client handoff.
Exact caller declarations and full JSON Schema validation are required; schema references cannot trigger network
or file reads. Missing tools, `tool_choice=none`, incompatible parameters,
binary writes and background shell ownership fail closed. Upstream approval or
sandbox hints never grant client permission. Bash uses the caller-declared tool's
MCP transport and original parameters. Native
Shell cannot faithfully represent generic client results and is not bridged.
The gateway does not wrap commands, adjust timeouts or infer shell state.
The retained client-wait limit remains three minutes; that is a protocol timeout,
not a client process lifetime or ownership promise.

The approved native lifecycle is: `generating → client tool_use → wait for
client tool_result → return native result to the waiting upstream → generating`.
No model-requested tool may execute on the gateway. Continuation ownership,
expiry, cancellation and result correlation must remain bounded and isolated
between authenticated callers. Existing declared MCP handoff can continue to
use its current history replay path.

Implementation status (2026-10-06): retained upstream continuation is implemented.
Keeping the original duplex allows missing-file Read → client Write → final
completion. Isolated prod-host probes of PR #2447 at `e6e36ba27` passed buffered
and SSE Read/Write/Bash/Grep with simulated client results and `composer-2.5`.
Grep requires the client schema to declare emitted parameters, including explicit
false `-i` and `multiline`; omitting those declarations correctly rejects the call.
The gateway tool target remained absent. These probes bypassed the production
scheduler and did not replay user 16's traffic. At the probe snapshot, production
remained on 1.8.272, account 150 was unschedulable, and its previous
`claude-fable-5-1` mapping was absent; protocol success is not production recovery.
Evidence: `.cache/observability/cursor150-pr2447-live/report.md`.

## Revised continuation contract (2026-10-04)

The approved review corrections replace native cancel/history replay with one
bounded run that owns its upstream connection. Each HTTP response consumes one
segment, ending at a client tool call or terminal usage. Closing a successfully
delivered handoff response does not cancel that run. A disconnected response,
expired wait, exhausted run budget or invalid continuation closes it. No local
executor, filesystem fallback, prompt/XML tool parser or public session API is
introduced. Previously working MCP-only replay remains supported.

An opaque, random tool ID identifies a pending operation, not an authorization
token. The owner is authenticated user + API key + account + resolved model;
the next request must also preserve the system and tool declarations. Full
history, when supplied, must match a rolling digest of the retained conversation;
result-only continuation is accepted without replaying a second conversation. Candidate
selection resolves pending ownership before ordinary scheduling and still applies
existing authorization and Plan gates. A live socket belongs to one process:
the current single-active-process account host resumes locally. A request on a
different process, after restart or after expiry fails closed with a neutral
continuation error; it must never silently start another run or fail over. This
version does not claim transparent socket migration or multi-replica recovery.
Results are consumed once; duplicates never replay an operation or settlement.
Pending runs have per-owner, per-account and global limits, a client-wait TTL,
and an absolute lifetime. IDs reveal neither supplier nor account identity.

Result adapters encode recognized missing-file errors and preserve other client
error text; upstream error rendering is not guaranteed to preserve classification.
Known line-numbered Read output is decoded; ambiguous/truncated output cannot be
used as complete file contents for an internal edit. Native Read/Write results
are sent with the original exec IDs. Bash results preserve text and `is_error`
through `McpResult`; process metadata is not inferred from arbitrary prose. Grep output
is accepted only when its structured native representation can be reconstructed.
Client results are never supplemented by reading gateway files or running tools.

### Gateway-only boundary clarification (conversation approval, 2026-10-06)

TokenKey retains model protocol state only. Client cwd, environment variables,
processes, files and permissions belong to the client. The gateway must not
create an execution environment, poll a background handle, fix generated shell
programs, or execute a fallback. Model prose about persistent Shell state is
not a gateway capability promise.

### Client Shell result protocol revision (conversation approval, 2026-10-07)

Prod-host wire capture showed that native Shell appends persistent cwd/env claims
and protocol timing as execution duration before the model sees the result. It
also renders generic client errors as spawn failures. The result protocol, not
just final prose, must change.

When client tools are declared, the upstream filter is fixed to
`x-cursor-agent-allowed-tools: mcp_tool_call,get_mcp_tools_tool_call`.
The second entry permits tool discovery; allowing only `mcp_tool_call` caused
`GET_MCP_TOOLS` failures in earlier production requests. Discovery is answered
from static caller declarations, not a running MCP server. The gateway never
learns additional allowed tools from upstream errors and never starts another
Run to repair this list. Requests without tools, including `tool_choice=none`,
do not enable the filter; their existing exec rejection boundary remains.

No Shell routing hint is added to the system prompt. Caller instructions,
descriptions, schemas, names and arguments are preserved. The existing MCP
identity normalization and retained connection carry client
Bash calls and `McpResult` text/`is_error`. Bash arguments must satisfy the caller's
schema before handoff; remote schema resolution remains disabled.
Untranslated Bash arguments follow the original schema, including open objects,
typed additional properties and local references. The native translator's
explicit-property whitelist does not restrict an unchanged client argument object.

MCP state queries use the official protocol's static server snapshot. Empty
filters or the `tokenkey` namespace return only current request declarations;
unknown namespaces return an empty snapshot. Both kick-only and waiting queries
return immediately without loading or contacting servers. `ready` describes
available declarations, not client execution health or permission. Client results
remain the only source of execution evidence.

Native Shell variants terminate at exec dispatch before any client operation is
published. HTTP 422 avoids ordinary 5xx failover; an already-started stream emits
a terminal error instead of successful completion. No native throw is sent to
invite another false spawn-error result, no new upstream run is replayed, and
no result is fabricated. This deliberately removes the old native Shell adapter,
including command wrapping and assumed success/zero. Other native tool bridges
and MCP-only history behavior are unchanged. AGENT mode remains enabled.

If the upstream ignores the tool filter and selects native Shell, this request
fails before handoff. The filter narrows the model's tool surface; dispatch
remains the enforced execution boundary. The gateway does not
promise that arbitrary model prose is accurate. In particular, supplier-generated
model-switch notices require separate structured evidence and are not filtered
or used to guess billing identity.

Controlled comparison and candidate evidence are retained under
`.cache/observability/cursor150-mcp-routing/`. Candidate ordinary/SSE cases covered
Bash success, client error and independent successive calls on prod account 150,
using simulated results and no command execution. They prove the candidate's
supplier protocol path, not production rollout or replay of user 16's client.

Further official-CLI comparison and prod-host probes are retained under
`.cache/observability/cursor150-metadata-routing/`. The CLI's documented
`x-cursor-agent-exclude-tools: shell_tool_call` did not suppress native Shell
in the tested upstream. A truthful native rejection alone did not reliably
produce client handoff. An upfront recovery contract allowed one handoff, but
another run failed during upstream tool discovery before client execution.
These recovery prototypes are not production behavior. No automatic retry,
native fallback or additional gateway execution capability is introduced.

The subsequent complete allowlist revision follows the protocol dependency
identified by [minimal-agent's upstream fix](https://github.com/gastonmorixe/minimal-agent-plugins/commit/eb1c76f7c835ce5caa8c2ae8bee9c243335b170a),
also used by magpie and cursor-rpc. In a prod-host A/B without routing hints,
the unfiltered request selected native Shell and failed with 422; the same
Shell task with both MCP entries reached client Bash and completed on the
original connection. Evidence is retained under
`.cache/observability/cursor150-mcp-allowlist/`. This does not promise that every
upstream version honors the filter or authorize enabling #150 scheduling.

A ranged Read retains content and the range marker but omits whole-file totals.
Proto3 zero defaults for these totals mean no supplied metadata in this adapter;
they do not prove an empty file or guarantee the upstream interprets them as
unknown. Unstructured Read/Write acknowledgements cannot
independently verify filesystem state. Grep text uses the first `:line:` separator
(filenames containing that separator remain unsupported); totals must not overflow.

Results with trailing user text are unsupported continuations and fail closed,
including after expiry. They must not silently create a new model run. System
and developer messages do not hide pending results from affinity lookup; only a
later assistant response separates completed tool history from a new user turn.
Concurrent submissions of one pending result are consumed once. This guards
gateway replay, not a model issuing a new operation with a new tool ID.

A retained run's intermediate tool segments report zero settled usage with the
provider-neutral `model-deferred` marker; both shared cost owners settle zero
(including per-request pricing), preserving normal usage records and hold release. Its final
reported usage is settled once through existing request billing. Abandoned or
failed runs remain unbilled, matching the existing failed-turn contract; no
estimate is charged and then charged again as cumulative reported usage. Pending
limits bound the extra exposure. This does not alter MCP-only replay settlement.

Acceptance includes Read, missing-file create, existing-file edit, Bash/Grep,
buffered/SSE protocol handoff, client refusal, duplicate results, expiry,
disconnect, process loss, tenant isolation and single settlement. All supplier
probes run on account 150's prod host; deployment and scheduling remain separate.

This design borrows the native-to-client handoff concept from
[raine v0.0.22](https://github.com/raine/claude-code-proxy/blob/88248df5739b7b3dcaa63a713227f1aeb2d205a8/src/providers/cursor/tool-bridge.ts),
without its local executors, Read re-read, or XML extraction.
Protobuf argument definitions follow the pinned oh-my-pi source recorded in
`agentpb/VENDORED_FROM.md`.

## Tool identity

The converter matches only against tools declared by the caller. It accepts the
wire `name`, `tool_name`, the logical name, and the known `mcp__tokenkey__`,
`mcp_tokenkey_`, and `tokenkey-` prefixes. The last spelling was observed in a
prod-host probe of account 150 with `claude-fable-5-1` on 2026-10-04.
Ambiguous or undeclared names fail closed. Tool call
IDs are normalized by the existing history ID owner.

## Public errors

Supplier diagnostics, native request IDs, account IDs, and raw messages remain
operator-only. Public errors use stable provider-neutral codes such as
`gateway_tool_unavailable`, `upstream_tool_protocol_error`,
`upstream_unavailable`, and `timeout_error`.

Error sanitization covers JSON bodies, SSE events, and returned Go errors;
`errors.Is` / `errors.As` still retain cancellation, timeout and policy evidence.
Native Shell incompatibility uses HTTP 422 as specified above. Other native HTTP
statuses and the shared policy/failover owners are unchanged.
Model fields retain the caller's public model name through the existing response
rewrite owner. Generated prose and caller-provided tool payloads are not rewritten.

Billing provenance on the wire uses `model-reported` / `model-estimated`,
plus `model-deferred` for retained intermediate tool segments.
The billing owner accepts both these labels and legacy `cursor-oauth-*` labels,
and persists the existing private labels. Rollout must upgrade the receiving
prod gateway before sending edges; rollback edges before the receiving gateway
so old receivers do not lose provenance from new labels.

## Owners and verification

- `backend/internal/integration/cursor/` owns native frame conversion,
  identity normalization, and bounded diagnostics.
- Existing protocol handlers own Messages/Chat/Responses output semantics and
  settlement.
- Tests must prove frame-level local-tool rejection continues text, declared
  MCP handoff round trips, unknown names fail closed, and public output does
  not contain supplier identifiers.

## Acceptance and risk coverage

| Risk / acceptance | Executable coverage |
| --- | --- |
| Unbridgeable workspace exec is rejected in AGENT mode; actual throw/close replies unblock text and usage | `TestAgentRunOutsideExecThrowsAndContinuesText` |
| Native tools map exact parameters, never touch gateway files or execute shell; fresh client results and errors reach the original exec | `TestNativeClientToolArguments`, `TestNativeClientToolsFailClosed`, `TestNativeToolsRetainedRoundTrip`, `TestCursorAdapterCannotImportLocalExecutors` |
| Declared name, tool_name and known aliases hand off; error tool_result history resumes | `TestMessagesToolAliasesHandoffAndReplay` and `TestCursorProtocolRoutesUseNativeTransportAndSettlement` |
| Unknown, ambiguous, conflicting and foreign-provider identities fail closed | `TestNormalizeDeclaredTool` and `TestMessagesToolProtocolErrorsAreNeutral` |
| JSON/SSE errors and public model/usage metadata remain supplier-neutral; failed turns do not settle | `TestMessagesPublicErrorMapping` and `TestCursorProtocolRoutesUseNativeTransportAndSettlement` |
| Owner/key/schema/history isolation, duplicate consumption, capacity, expiry and disconnect | `TestNativeRunIsolationExpiryAndCancellation`, `TestPendingToolIDsOnlyLatestResultTurn` |
| Read wrappers, missing/empty file, structured results and client rejection | `TestNativeClientResultSemantics` |
| Original client errors, ranged metadata, Grep fidelity, trailing-input rejection and concurrent single consumption | `TestNativeClientResultEvidence`, `TestClientBashErrorContinuesOriginalRun`, `TestNativeContinuationTrailingTextFailsClosed`, `TestNativeContinuationConcurrentSingleConsumption` |
| Client Bash declarations and errors preserved; native Shell rejected before handoff without execution or failover | `TestClientToolRoutingPreservesDeclarations`, `TestClientBashRejectsInvalidArguments`, `TestNativeShellFailsBeforeClientHandoff`, `TestCursorProtocolRoutesUseNativeTransportAndSettlement` |
| Mixed known/unknown exec variants reject in-band without client handoff or context disclosure | `TestAgentRunAmbiguousExecRejectsClientHandoff` |
| Client schema semantics preserved without external schema fetch; static metadata discovery continues to client handoff on the same connection | `TestClientBashPreservesCallerSchemaSemantics`, `TestClientToolMetadataSnapshot`, `TestClientToolMetadataContinuesToClientHandoff` |
| Fixed invocation/discovery allowlist; no filter when tools are absent/disabled; upstream errors never expand the list or replay | `TestAgentClientToolAllowlistIncludesDiscovery`, `TestClientToolFilterDisabledWithoutDeclarations`, `TestClientToolAllowlistDoesNotLearnOrReplay` |
| Zero intermediate cost including per-request pricing; final settlement remains normal | `TestCursorDeferredSegmentsSettleZeroBeforeTerminalUsage` |
| Cancellation/timeout identity, partial output, policy and transport regressions | Existing Cursor transport, timeout and buffered failure tests |
| Legacy and neutral wire provenance retain the same durable billing tier | `TestCursorRelayConversionsRetainBillingProvenance` |

Commands (from `backend/`):

```sh
go test -race ./internal/integration/cursor
go test -tags=unit ./internal/service -run 'TestCursor|TestOpenAI.*Transport|TestMappedResponseModel' -count=1
```

This change has no UI artifact (`no-web-impact`). Local verification uses native
duplex fixtures and service protocol integration tests. Isolated prod-host probes
exercise real supplier connections with simulated client tool results; they are
not UI e2e or a replay of user 16's production traffic. Candidate verification does not deploy the change or enable account scheduling.
Client permission denials, unsupported tool schemas, upstream policy refusals and
expired/process-lost continuations remain explicit failure boundaries. Model
prose is not filtered, so supplier-neutral protocol metadata does not guarantee
that arbitrary generated text never names a supplier or recommends a mode.
