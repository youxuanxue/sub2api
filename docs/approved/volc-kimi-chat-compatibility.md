---
title: Volc Agent Plan Kimi Chat compatibility
status: approved
approved_by: "feng (2026-10-15 conversation: 同意。请修复。)"
approved_at: 2026-10-15
risk: high
---

# Volc Agent Plan Kimi Chat compatibility

## Problem and approval

The cn-prod customer capability checks exposed two supplier-specific failures:
Kimi K3 thinking-off was silently changed to low effort, and tool selectors were
rejected. The user approved fixing thinking normalization and allowed-tools
conversion after reviewing the production logs and direct upstream controls.
This approval covers implementation and validation, not production deployment.

## Evidence

2026-10-15 cn-prod customer requests `671eaef7-32ef-4778-86d6-44230ee9c8db`
and `677209b1-4796-4d37-8cc3-aa591b9b42c8` failed at 19:26:59 Asia/Shanghai.
The first received a Volc InvalidParameter 400; the second was rejected locally
by the named-tool parser. Subsequent thinking checks used supplier account #16.
IDs identify evidence only, never capability selectors.

Direct synthetic controls against `/api/plan/v3/chat/completions`:

- Volc Kimi K3 `thinking.type=disabled` returns no reasoning content.
- Boolean `enable_thinking=false` and template aliases alone still return reasoning.
- Named `tool_choice` with default thinking returns 400; with thinking disabled it
  returns 200 and the named tool call.
- A single declared tool with `tool_choice=required` returns 200 and that tool call
  with thinking still enabled. `auto` also works.
- Volc rejects `tool_choice.type=allowed_tools` natively.

Private raw evidence: `~/.local/share/tokenkey/cn-kimi-diagnosis-20261015-210522/`.
No customer prompt content or credentials are included in this repository.

## Behavior and owners

Only the evidenced Volc Agent Plan endpoint identity and Kimi K3 family use this
policy. Existing supplier recognition owns endpoint identification; names and
account IDs are not selectors. Other suppliers retain their existing policy.

`service/volc_kimi_chat_constraints_tk.go` owns supplier request normalization;
`applyMoonshotThinkingShape` passes the selected account and routes to that owner
before applying the historical Moonshot default. Every existing raw/bridge Chat
call site passes the selected account, including converted Chat and admin probes.
This does not change protocolrouter.Plan, account selection, billing or schema.

- Canonical `thinking.type` is preserved. Boolean `thinking`, `enable_thinking`,
  and `chat_template_kwargs.{thinking,enable_thinking}` are normalized to the
  canonical on/off intent. Conflicting or malformed switches return a field-level
  400. Unrelated body fields and retry input are preserved.
- Default thinking and `reasoning_effort` remain untouched. Closing thinking must
  not silently become low effort. No response reasoning is hidden to fake success.
- Named function selection is reduced to its validated single definition plus
  `required`, preserving forced-tool semantics and thinking.
- `allowed_tools` accepts named function references and `auto` or `required`.
  The outgoing definitions are restricted to exactly the allowed set; mode and
  tool result history are preserved. Missing/duplicate references, unsupported
  tool types, empty sets and legacy function API combinations fail explicitly.
- Existing `auto`, `required`, `none` and ordinary tool round trips retain their
  existing behavior. This patch does not claim native allowed-tools support.

## Validation and rollout

`TestVolcKimiThinkingSwitchesAndSupplierScope` exercises on/off normalization,
conflicts, supplier/model scope, idempotence and precision/input preservation.
`TestVolcKimiToolSelectionPreservesSemantics` exercises allowed subsets, forced
function, mode preservation, invalid references and history preservation.
`TestVolcKimiForwardKeepsThinkingAndToolConstraints` checks raw forwarding and the
bridge boundary for streaming and buffered calls. Existing Moonshot and provider
tool constraint tests must still pass. The gateway sentinel registry anchors the
supplier owner, selected-account routing and these regression tests.

On 2026-10-15, synthetic requests produced by the changed Go normalizer were sent
directly to the live Volc upstream. Named selection and allowed-tools auto/required
returned 200, selected only the allowed function and preserved reasoning. Canonical
thinking-off, `enable_thinking=false` and both template aliases returned 200 with
no reasoning content. Full backend unit tests and worktree preflight passed.
Private evidence: `~/.local/share/tokenkey/volc-kimi-fixed-wire-20261015-212144/`.
These checks validate the repaired wire shape; they are not deployed gateway smoke.

After release, use an isolated administrator probe to verify the outgoing request
and actual reasoning/tool output, then rerun the customer's capability matrix.
HTTP 200 alone is insufficient evidence. Rollback uses the normal release path.
