---
title: Volc Agent Plan Kimi Chat compatibility
status: approved
approved_by: "feng (2026-10-15 conversation: 同意。请修复。); feng (2026-10-28 conversation: 空 thinking.type 直打上游成功，请修复网关误伤。); product owner (2026-11-04 conversation: 如果上游就返回 usage 为 0，我们网关可以通过估算兜底吧。不然就亏了。); feng (2026-11-05 conversation: 同意 1 修复建议：只改报错，状态码不变；修复 video_url 字符串的问题；第3项，实现。); feng (2026-11-06 conversation: cn-prod 应该是开启了 qa 的，你再深挖下日志，这个 kimi-k3 的 6% 的错误率很重要，需要尽快排查、修复。); feng (2026-11-12 conversation: 同意修复 1。); feng (2026-11-13 conversation: 按照 KVV 要求修复 1-3。); feng (2026-11-13 conversation: 按照 KVV 要求修复 4-8。); feng (2026-11-13 conversation: kimi-k3 / kimi-k3-* / kimi-k3.1-pro / kimi-* 都需要生效。)"
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
policy. The family is `kimi-k3`, `kimi-k3-*`, and `kimi-k3.*` (`kimi-k3.1-pro`
included, `kimi-k30` excluded). `kimi-k3.1-pro` takes the same Volc normalizer,
including `n=1`, and the same Moonshot dynamic-tool promotion. The DashScope
sampling pin stays on `kimi-k3` and `kimi-k3-*` only. On 2026-11-13 the user
decided 「按照 KVV 要求」, and then 「kimi-k3 / kimi-k3-* / kimi-k3.1-pro / kimi-*
都需要生效」. The 400s for orphan tool messages, blank function names, KVV
sampling values, and illegal dynamic tools therefore apply to every normalized
id that starts with `kimi-`. `moonshot-*` is outside that set. An omitted
`top_p` is still written as 0.95, and a legal client `temperature` may still
be pinned to 1.0, only on the DashScope pin's narrower set. A KVV-rejected
sampling value is not rewritten into a legal one.
Existing supplier recognition owns endpoint identification; names and
account IDs are not selectors. Other suppliers retain their existing policy.

`service/volc_kimi_chat_constraints_tk.go` owns supplier request normalization;
`applyMoonshotThinkingShape` passes the selected account and routes to that owner
before applying the historical Moonshot default. Every existing raw/bridge Chat
call site passes the selected account, including converted Chat and admin probes.
This does not change protocolrouter.Plan, account selection, billing or schema.

- Canonical `thinking.type` is preserved. Boolean `thinking`, `enable_thinking`,
  and `chat_template_kwargs.{thinking,enable_thinking}` are normalized to the
  canonical on/off intent. Conflicting or malformed switches return a field-level
  400. A missing or blank `thinking.type` (`""` or whitespace) is unspecified,
  not malformed: it must not 400. When `reasoning_effort` is also present, the
  effort rule below rewrites that blank type to `enabled`. Unrelated body
  fields and retry input are preserved.
- 2026-10-28 cn-prod user 2 sent `thinking: {"type":""}` with `reasoning_effort`
  (`max` / `high` / `xhigh` / `medium`). The gateway returned 400 before relay.
  Direct replay of two captured bodies to Volc Agent Plan (accounts 77 and 11,
  output capped at 16 tokens, stream off) returned HTTP 200 with
  `reasoning_tokens`. That local 400 was a false reject.
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
- Moonshot dynamic tool injection is owned by `applyMoonshotK3DynamicToolsShape`.
  The check runs for every `isKimiModel` request (`kimi-k3`, `kimi-k3-*`,
  `kimi-k3.1-pro`, and the rest of `kimi-*`, including `kimi-k30` and
  `kimi-k2.5`) before promotion and before account selection. `moonshot-*` is
  returned unchanged. A `system` message whose content is absent, JSON null, an empty
  string, or whitespace only, and whose tools are legal, is still promoted to
  top-level `tools` and that message is dropped. Several such messages promote
  together. Distinct names still merge with top-level `tools`, including a
  128-character name. When the client omitted `tool_choice`, the gateway still
  sets `required`. A client-supplied `tool_choice` is kept. This keeps
  Calculator-style admission probes from failing with missing
  `messages.content[.text]`.
  On 2026-11-13 the user decided 「按照 KVV 要求」to fix cases 4–8. These
  requests return 400, `invalid_parameter`, before promotion and are not
  forwarded. Callers that have a gin context write that 400 and stop, using the
  same path as `applyMoonshotThinkingShape`. Client-visible text does not name
  the supplier.
  - Any non-system message (`user`, `assistant`, or another role) that has a
    `tools` field: param `messages`.
  - A system message that has `tools` and content outside the empty forms above
    (a non-empty string, a non-empty array, or any other content): param
    `messages`.
  - A dynamic tool missing `type`, `function`, or `function.name`: param
    `tools`. An empty or whitespace `function.name` is not this case. Promotion
    still lifts it, and the empty-name check below returns 400 on param `tools`.
  - A message `tools` value that is not an array (including an object), or an
    array element that is not an object (including null): param `tools`.
  - The same function name, compared after trimming whitespace, inside one
    message, across messages, or between a dynamic tool and top-level `tools`:
    param `tools`. The later declaration is not silently dropped.
- 2026-10-23 cn-prod (Tencent Beijing) user 2 kimi-k3 400s, checked against the
  captured request shape and a direct replay on account 88:
  a plain object in `messages.content` (`{"key":"val"}`) is sent as its JSON
  text; one content part sent as an object (`{"type":"text","text":"hi"}`) is
  wrapped in an array. Both are 400 on the wire and 200 after this rewrite.
  A `tool` message whose `tool_call_id` is missing, blank, or matches no
  assistant `tool_calls` id returns 400, `invalid_parameter`, param
  `messages`. A tool message whose id matches is forwarded unchanged.
  Historical `tool_calls` that lack `function.name` stay a 400; they are not
  rewritten into a user message. A function tool whose `name` is empty or
  whitespace returns 400, param `tools`. That check runs after Moonshot
  dynamic-tool promotion, so a system message with empty content and
  `function.name: ""` is still rejected once the tool is on top-level
  `tools`. A non-empty name is unchanged. The previous rewrite (orphan tool
  became a user message and returned 200; empty names were dropped and the
  request continued) is retired by the 2026-11-13 decision 「按照 KVV 要求」.
  Matched tool results, string content, and content-part arrays stay as sent.
  An https `video_url` the gateway cannot fetch (captured Wikimedia webm from
  Beijing; the same unnamed InvalidParameter was reproduced with
  `https://example.com/tiny.mp4`) is rejected locally before relay:
  `invalid_parameter`, param `messages.content`, asking for a video URL this
  service can access. A string `video_url` (Moonshot documents it as
  equivalent to `video_url.url`) is rewritten to `{"url": ...}`; Volc returned
  400 "expected an object" for the string form (cn-prod 2026-11-05 user 2).
  Inline `data:` payloads, supplier schemes such as
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

## 2026-11-04 request-field parity (cn-prod kimi-k3 benchmark)

The 2026-11-04 cn-prod benchmark (`/v1/chat/completions`, model `kimi-k3`,
Volc Agent Plan supply) used the supplier baseline rule that inputs, outputs
and behavior must match the Moonshot official API. Several fields returned
HTTP 200 while the supplier ignored or reinterpreted them. Scope is unchanged:
only the Volc Agent Plan endpoint plus the Kimi K3 family. Owner:
`service/volc_kimi_chat_request_limits_tk.go`, called first from
`normalizeVolcKimiChatRequest`. All rejects use the existing
`newOpenAIInvalidParameterError` (`invalid_parameter` + `param`) and stop
before relay. Valid values are forwarded byte-for-byte.

Sources: Moonshot Chat API (<https://platform.kimi.ai/docs/api/chat>) and
Model Parameter Reference (<https://platform.kimi.ai/docs/api/models-overview.md>),
read 2026-11-04.

- `reasoning_effort` `none` (also `off` / `disabled`, top-level or nested
  `reasoning.effort`) now means `thinking.type=disabled` and strips the effort
  field. Moonshot's K3 enum is `low|high|max` with no off value, so Moonshot
  itself has no `none`. This supplier has a real off-switch (2026-10-15
  evidence above), and this doc already says closing thinking must not
  silently become low effort. The previous `none`→`low` mapping ran with
  reasoning on, which was the observed bug. `none` combined with an explicit
  enabled switch returns 400 on `reasoning_effort`. `minimal` stays `low`.
  Other suppliers keep the historical `none`→`low` mapping.
- 2026-11-13 user decision 「按照 KVV 要求」 replaces the earlier choice to
  accept every in-range `temperature` and `top_p`. The fixed-value check is
  `rejectKimiK3FixedSampling`, called from `applyMoonshotThinkingShape` before
  the account branch for every `isKimiModel` id (`kimi-k3`, `kimi-k3-*`,
  `kimi-k3.1-pro`, and the rest of `kimi-*`). A non-matching account still
  returns 400 for these fields. Comparison uses a small float tolerance.
  Accepted values
  are forwarded unchanged by the normalizer:
  omitted fields; `temperature` in `[0, 2]` except `1.1` and `2.0`
  (including 0, 0.6 and 1.0); `top_p` exactly 0.95; `presence_penalty` 0;
  `frequency_penalty` 0; `n` 1.
  Rejected with 400 on that field: `temperature` 1.1 or 2.0, `temperature`
  outside `[0, 2]` (including -0.1) or non-numeric; `top_p` other than 0.95
  (including 0, 0.8 and 1) or non-numeric; `presence_penalty` or
  `frequency_penalty` other than 0; `n` other than 1.
  The DashScope pin runs before `applyMoonshotThinkingShape` on the bridge.
  It still writes `top_p` 0.95 when the client omitted it, and it may pin a
  legal `temperature` to 1.0. It must leave a rejected value in place so the
  bridge returns 400. The raw Chat path does not run the pin and returns the
  same 400. `kimi-k3.1-pro` and other `kimi-*` ids use this same table.
  Their legal temperatures, including 0.6, stay as the client sent them
  because the DashScope pin does not rewrite those ids.
- `n` other than `1` returns 400 on `n` for every `kimi-*` id. Moonshot fixes K3 `n` at `1`; the
  supplier returned one choice for `n=2`.
- `logprobs=true` or `top_logprobs>0` returns 400 on that field. Moonshot
  documents `logprobs` and `top_logprobs` (0 to 20). The supplier returned
  200 with no logprobs, so the gateway cannot deliver them. `logprobs=false`
  and `top_logprobs=0` still pass.
- `max_tokens` / `max_completion_tokens` must be a positive integer. Volc
  treats `0` as unset and answered normally. Moonshot documents no minimum,
  but zero output tokens is not a usable limit, and `-1` was already an
  upstream 400. Converters emit these fields only when positive, so this
  checks client input only.
- Client-visible error messages drop a trailing supplier trace id of the form
  `Request id: <16+ alphanumerics>` (for example
  `... Request id: 021793796431474c...`). The strip runs inside the shared
  `TkSanitizeUpstreamProductWireMessage` choke point, so every writer that
  already calls it is covered. Ops keeps the original message because
  `setOpsUpstreamError` records it before the client writer runs. Other
  relays' parenthesized `(request id: ...)` envelopes are not touched.

### Known limitation: `stop` in thinking mode (no gateway change)

With thinking on, `stop:["5"]` and the prompt "Count from 1 to 10, separated by
commas" returned empty `content` with `finish_reason=stop`. The stop sequence
matched inside `reasoning_content`. Moonshot documents `stop` as "max 5
strings, each at most 32 bytes; matched text is not output" and does not say
whether it applies to reasoning. The gateway forwards `stop` unchanged and
the match happens during supplier decoding, so the gateway cannot change it.
Rejected options: removing `stop` and truncating the answer locally (changes
streaming and token accounting, and fakes supplier behavior), and turning
thinking off when `stop` is present (silently changes the request). Behavior
is unchanged. Clients that need answer-only stop sequences should send
`thinking.type=disabled` or pick stop strings that do not appear in
reasoning. Evidence still needed: a direct Volc Agent Plan replay of the same
body with `thinking.type=enabled` and with `disabled`, to confirm
supplier-side matching and whether Volc documents an answer-only stop
option.

Tests: `TestVolcKimiRequestLimitsRejectUndeliverableFields`,
`TestVolcKimiReasoningEffortNoneDisablesThinking`,
`TestVolcKimiRequestLimitsForwardReturnsLocal400` (no relay on reject),
`TestVolcKimiUpstream400HidesSupplierRequestID` (wire hides the id, ops keeps
it), and `TestTkSanitizeUpstreamProductWireMessageStripsSupplierRequestID`.
`TestVolcKimiFallbackReasoningWireAndUsage` now expects `none` to reach the
wire as `thinking.type=disabled`.

## 2026-11-04: zero upstream usage is billed from a local estimate

### Evidence

A cn-prod kimi-k3 benchmark (2026-11-04 12:34–12:58 UTC) returned HTTP 200 with
correct content and `usage` all zero, and the request was billed $0. QA captured
upstream responses for two non-stream requests showing `finish_reason=stop`,
correct content and `prompt_tokens=completion_tokens=total_tokens=0` with zero
cached and reasoning detail. A control request on the same account pool in the
same minute reported normal usage. Two streaming captures end with `[DONE]` and
carry no usage object at all, even though the gateway forces
`stream_options.include_usage=true`.

The thinking parameters are not the cause. Over 24 hours of kimi-k3 traffic,
4 Agent Plan accounts returned zero usage on 100% of requests (156–334 requests
each), 3 more on 96–99%, 30 more on 5–95%, and 78 on under 5%. The fully-zero
accounts started zeroing at 2026-11-03 about 13:55 UTC. All of them have the same
channel type, base URL and credential layout. On those same accounts,
deepseek-v4.1-flash, deepseek-v4-flash and doubao-seed-2.1-lite showed 0.0% zero
usage. The fault is on the Volc side, specific to kimi-k3, and varies per
account. Fleet-wide, 12–29% of kimi-k3 usage rows per hour had zero tokens and
zero cost. Offline gateway tests (direct and universal candidate routes,
stream and non-stream, every thinking/effort shape) keep real upstream usage
intact, so the gateway was recording what upstream sent.

### Behavior (moved)

The estimate is no longer Volc-specific. Product decision 2026-11-05 made it one
gateway-wide owner for every platform and text model. Behavior, owners, the
`usage-estimated` billing tier, the `usage_estimated` ops event, accuracy and
tests now live in
[`usage-estimate-fallback-ssot.md`](usage-estimate-fallback-ssot.md). The Volc
raw Chat path only stashes its final outbound body for the owner. The
unreleased `volc-kimi-estimated` tier and `volc_kimi_usage_estimated` event
were never written in production and were retired with this move.

Follow-ups, not in this change: temporarily cooling kimi-k3 on accounts that
persistently return zero usage. A bounded model-scoped cooldown already exists
(`SetModelRateLimit`), but a trigger threshold needs a reviewed policy. Also
report the affected accounts to the supplier.

## 2026-11-05: tool_choice errors, string video_url, prediction shape

Customer user 2 capability matrix on cn-prod 1.8.346, then direct controls on
Volc Agent Plan account 116 (`max_tokens=16`, stream off). Sources: Moonshot
Chat API (<https://platform.kimi.ai/docs/api/chat>), read 2026-11-05.

- `tool_choice` outside `none|auto|required|named function` (for example
  `"default"`) stays HTTP 400. Moonshot and OpenAI list the same enum, and Volc
  returns 400 "supported values are: auto, none, required" with or without
  tools. Only the error changes: `parseChatToolConstraint`
  (`openai_chat_constraints_tk.go`, shared with Ali Token Plan) returns a
  field-level `invalid_parameter` with `param=tool_choice` (or
  `function_call`) naming the allowed values, and the Chat writers emit it
  through `writeChatProviderRequestError`. The Responses fallback keeps its own
  error shape.
- A string `video_url` is rewritten to `{"url": ...}` (see the 2026-10-23
  bullet). Direct control: the same URL as a string is 400 "expected an
  object"; as an object it is 200 with the video tokenized (7172 prompt tokens).
- `prediction` must match Moonshot: an object, `type` exactly `content`,
  `content` a string or an array of text parts. Otherwise a field-level 400 on
  `prediction`, `prediction.type` or `prediction.content`. Volc returned 200
  with identical prompt tokens for valid, bogus-type, missing-type, string and
  numeric-content shapes, so it ignores the field. Valid predictions are still
  forwarded: predicted output changes latency, not the answer. Impact before
  release: of 32,592 captured kimi requests (2026-11-04 03:00 to 2026-11-05
  16:00 Asia/Shanghai) only 44 carried `prediction`, all from user 2's
  capability tests; 6 had a non-`content` type and become 400.

Tests: `TestChatToolConstraints_InvalidModeIsFieldLevel`,
`TestVolcKimiInboundMessageShapes` (string video_url),
`TestVolcKimiRequestLimitsRejectUndeliverableFields` (prediction shapes).

## 2026-11-06: empty text content parts

Evidence: cn-prod (Tencent Beijing, 1.8.350), 24 hours to 2026-11-06. User 2
received 921 final 400s with the unnamed Volc `InvalidParameter` (`param:""`)
on `/v1/chat/completions` kimi-k3, spread over about 180 Agent Plan accounts,
with one attempt each. QA captures were compared for 800 failing and 800
successful requests from the same user and window, using key/type summaries only.
153 of 433 fully captured failures, and 317 of 367 failures whose captured
body was cut at the 2 MiB capture limit, contain a
`{"type":"text","text":""}` part. None of the 800 successes do. The client
sends two system messages as part arrays, and the second one starts with an
empty text part.

Direct controls on one Agent Plan account (`max_tokens=16`) returned 400
`InvalidParameter` for an empty text part in a system, user, assistant or
tool message, alone or next to non-empty text, both stream and non-stream.
Whitespace text (`" "`) and string content `""` returned 200. Two captured
failing bodies were replayed: as sent they returned 400, and with only the
empty part removed, or with the parts merged into one string, they returned 200.

Behavior: `normalizeVolcKimiMessageContents` drops text parts whose text is
exactly `""` in every role. If no parts remain, the content becomes `""`.
The part contributes no input, so the request means the same thing. Other
parts, part order and whitespace text are kept. Bodies without empty parts
are forwarded byte-for-byte. Other suppliers are unchanged.

Not covered: the remaining failures with no empty part. Direct replay of
captured large bodies from this group (318 or 64 tools, 58 to 131 messages)
returned 200 with the same thinking/effort shape, so their cause is still
open. `tool_choice:"required"` with no tools is also an unnamed upstream 400
(4 sampled requests); it is not rewritten here.

Test: `TestVolcKimiDropsEmptyTextParts`.
