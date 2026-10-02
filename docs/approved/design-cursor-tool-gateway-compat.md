---
title: Provider-neutral Cursor tool gateway compatibility
status: approved
approved_by: "feng (conversation approval, 2026-10-01)"
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
| Native shell/read/grep/write/agent exec | `ExecClientThrow + StreamClose`, public capability error, continue the stream |
| Declared MCP/function tool | Normalize identity, emit standard tool handoff, cancel the native turn, replay result on the next request |
| Unknown MCP tool | Return a bounded provider-neutral protocol error; never execute or silently pass through |
| Text/thinking/usage | Convert through the existing Messages/Chat/Responses owners |

Native exec rejection is a frame-level response, not an HTTP request failure.
The server must continue reading the stream so the model can answer without the
workspace tool. If the model cannot proceed, the resulting limitation is
reported as an ordinary capability error.

## Tool identity

The converter matches only against tools declared by the caller. It accepts the
wire `name`, `tool_name`, the logical name, and the known `mcp__tokenkey__` and
`mcp_tokenkey_` prefixes. Ambiguous or undeclared names fail closed. Tool call
IDs are normalized by the existing history ID owner.

## Public errors

Supplier diagnostics, native request IDs, account IDs, and raw messages remain
operator-only. Public errors use stable provider-neutral codes such as
`gateway_tool_unavailable`, `upstream_tool_protocol_error`,
`upstream_unavailable`, and `timeout_error`.

Error sanitization covers JSON bodies, SSE events, and returned Go errors;
`errors.Is` / `errors.As` still retain cancellation, timeout and policy evidence.
Native HTTP statuses and the shared policy/failover owners are unchanged.
Model fields retain the caller's public model name through the existing response
rewrite owner. Generated prose and caller-provided tool payloads are not rewritten.

Billing provenance on the wire uses `model-reported` / `model-estimated`.
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
| Native workspace exec stays opaque; ASK mode and no native allowlist; actual throw/close replies unblock text and usage | `TestAgentRunOutsideExecThrowsAndContinuesText` |
| Declared name, tool_name and known aliases hand off; error tool_result history resumes | `TestMessagesToolAliasesHandoffAndReplay` and `TestCursorProtocolRoutesUseNativeTransportAndSettlement` |
| Unknown, ambiguous, conflicting and foreign-provider identities fail closed | `TestNormalizeDeclaredTool` and `TestMessagesToolProtocolErrorsAreNeutral` |
| JSON/SSE errors and public model/usage metadata remain supplier-neutral; failed turns do not settle | `TestMessagesPublicErrorMapping` and `TestCursorProtocolRoutesUseNativeTransportAndSettlement` |
| Cancellation/timeout identity, partial output, policy and transport regressions | Existing Cursor transport, timeout and buffered failure tests |
| Legacy and neutral wire provenance retain the same durable billing tier | `TestCursorRelayConversionsRetainBillingProvenance` |

Commands (from `backend/`):

```sh
go test ./internal/integration/cursor
go test -tags=unit ./internal/service -run 'TestCursor|TestOpenAI.*Transport|TestMappedResponseModel' -count=1
```

This change has no UI artifact (`no-web-impact`); verification uses native
duplex fixtures and service protocol integration tests, not UI e2e or live
supplier probes. Production behavior remains unverified until rollout.
