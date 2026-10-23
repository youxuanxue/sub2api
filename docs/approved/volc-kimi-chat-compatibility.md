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
- Public kimi-k3 surface stays Moonshot-compatible: `reasoning_effort` accepts
  `low|high|max` plus common aliases (`highest`→`max`, `medium`→`high`, nested
  `reasoning.effort`, etc.). Unknown effort tokens return a field-level 400.
  When effort is present and thinking is not explicitly disabled, inject
  `thinking.type=enabled` so intensity probes observe reasoning content instead
  of silent 200s. Explicit `thinking.type=disabled` still wins over effort and
  strips `reasoning_effort` before relay (upstream rejects the combination).
  Anthropic-style `thinking.budget_tokens` is stripped before relay. Closing
  thinking must not silently become low effort. No response reasoning is hidden
  to fake success. Client-visible errors must not name the upstream supplier.
- Named function selection is reduced to its validated single definition plus
  `required`, preserving forced-tool semantics and thinking.
- `allowed_tools` accepts the Chat nested `tool_choice.allowed_tools` object and
  the flattened compatibility form, with named function references and `auto` or
  `required`. Mixed nested/flat forms are rejected rather than choosing a policy.
  The outgoing definitions are restricted to exactly the allowed set; mode and
  tool result history are preserved. Missing/duplicate references, unsupported
  tool types, empty sets and legacy function API combinations fail explicitly.
- Existing `auto`, `required`, `none` and ordinary tool round trips retain their
  existing behavior. This patch does not claim native allowed-tools support.
- Moonshot dynamic tool injection (`system` message carrying `tools` without
  usable content — absent, null, or empty/whitespace string) is promoted to
  top-level `tools` before relay so Calculator-style admission probes do not
  fail with missing `messages.content[.text]`.
- 2026-10-23 cn-prod (Tencent Beijing) user 2 kimi-k3 400s, checked against the
  captured request shape and a direct replay on account 88:
  a plain object in `messages.content` (`{"key":"val"}`) is sent as its JSON
  text; one content part sent as an object (`{"type":"text","text":"hi"}`) is
  wrapped in an array. Both are 400 on the wire and 200 after this rewrite.
  A `tool` message whose `tool_call_id` matches no assistant `tool_calls` id
  becomes a user message and keeps its content (`tool_call_id is not found`
  → 200). Function tools with an empty name are dropped; if none remain,
  `tool_choice` is dropped with them (`function name is invalid` → 200).
  Matched tool results, string content, and content-part arrays stay as sent.
  An https `video_url` the gateway cannot fetch (captured Wikimedia webm from
  Beijing; the same unnamed InvalidParameter was reproduced with
  `https://example.com/tiny.mp4`) is rejected locally before relay:
  `invalid_parameter`, param `messages.content`, asking for a video URL this
  service can access. Inline `data:` payloads, supplier schemes such as
  `ms://`, and URLs the preflight can read are still forwarded. Do not strip
  video parts. A body that exceeds the context window is not truncated.

## Validation and rollout

`TestVolcKimiThinkingSwitchesAndSupplierScope` exercises on/off normalization,
conflicts, supplier/model scope, idempotence and precision/input preservation.
`TestVolcKimiReasoningEffortMoonshotCompatible` exercises effort aliases,
effort-implied thinking enablement, disabled-wins-over-effort, and invalid effort.
`TestVolcKimiToolSelectionPreservesSemantics` exercises allowed subsets, forced
function, mode preservation, invalid references and history preservation.
`TestVolcKimiChatAllowedToolsEnvelope` checks the Chat SDK envelope, multiple
allowed tools, definition ordering and malformed/mixed envelopes.
`TestVolcKimiForwardKeepsThinkingAndToolConstraints` checks raw forwarding, the
customer dispatcher and admin probes for streaming and buffered calls.
`TestVolcKimiVideoURLPreflight` and `TestVolcKimiHTTPVideoProbe` check that an
unreachable `video_url` becomes the local parameter error, while a readable
URL, an inline data URL and a non-Volc request keep the video part.
Existing Moonshot and provider
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
