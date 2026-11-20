---
title: Volc Agent Plan Kimi Chat compatibility
status: approved
approved_by: "feng / product owner（2026-10-15 起多次会话批准；细节见 git 历史 frontmatter）"
approved_at: 2026-10-15
risk: high
revised: 2026-11-20（再压按日 Evidence；保留行为契约与 Amendments）
---

# Volc Agent Plan Kimi Chat compatibility

## Problem and approval

Supplier-specific Kimi K3 failures on Volc Agent Plan（thinking / tool selectors）
获批修复与验证；不含生产部署授权。

## Behavior and owners

Only the evidenced Volc Agent Plan endpoint identity and Kimi K3 family use this
policy. The family is `kimi-k3`, `kimi-k3-*`, and `kimi-k3.*` (`kimi-k3.1-pro`
included, `kimi-k30` excluded). `kimi-k3.1-pro` takes the same Volc normalizer,
including `n=1`, and the same Moonshot dynamic-tool promotion. The DashScope
sampling pin stays on `kimi-k3` and `kimi-k3-*` only. KVV：orphan tool、空白 function name、非法 sampling、非法 dynamic tools 的 400
适用于每个规范化后以 `kimi-` 开头的 id。 `moonshot-*` is outside that set. An omitted
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
- Public kimi-k3 surface stays Moonshot-compatible: `reasoning_effort` accepts
  `low|high|max` plus common aliases (`highest`→`max`, `medium`→`high`, nested
  `reasoning.effort`, etc.). Unknown effort tokens return a field-level 400.
  When effort is present and thinking is not explicitly disabled, inject
  `thinking.type=enabled` so intensity probes observe reasoning content instead
  of silent 200s. Explicit `thinking.type=disabled` still wins over effort and
  strips `reasoning_effort` before relay (upstream rejects the combination).
  When
  `thinking.effort` is present and thinking is not disabled, that value wins:
  it is written to `reasoning_effort` (the field this supply records in the
  system prompt) and a conflicting client `reasoning_effort` is replaced.
  `thinking.keep` and `thinking.type` stay. An unknown `thinking.effort` token
  returns 400 on param `thinking.effort`. Disabled thinking still ignores
  `thinking.effort`. A request that only sets `reasoning_effort` is unchanged.
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
  Illegal dynamic-tool shapes return 400, `invalid_parameter`, before promotion and are not
  forwarded. Callers that have a gin context write that 400 and stop, using the
  same path as `applyMoonshotThinkingShape`. Client-visible text does not name
  the supplier.
  A kimi
  request whose top-level `tools` include `function.parameters`, and whose
  upstream 400 message is exactly “a parameter specified in the request is not
  valid”, gets `: tool function parameters schema` appended on the client
  wire. Ops keeps the original sentence. Other 400 text, requests without tool
  parameters, and non-kimi models are unchanged. The status code stays 400.
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
- A plain object in `messages.content` is sent as its JSON text; a lone content
  part object is wrapped in an array.
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
  On 2026-11-17 cn-prod `tokensea_online` (user 2) live ablation showed Volc
  400 `InvalidParameter` when an assistant text turn was inserted between
  `assistant.tool_calls` and the matching `tool` result(s). Tool schemas alone
  (including Workflow) replayed 200. The gateway drops those interleaved
  assistant-without-`tool_calls` messages before relay; ordinary assistant
  text outside a tool round-trip is unchanged. The client-visible
  `: tool function parameters schema` suffix remains a heuristic for the
  generic upstream sentence when tools are present and is not proof of schema
  recursion.
  Matched tool results, string content, and content-part arrays stay as sent.
  An https `video_url` the gateway cannot fetch is rejected locally before relay:
  `invalid_parameter`, param `messages.content`, asking for a video URL this
  service can access. A string `video_url` (Moonshot documents it as
  equivalent to `video_url.url`) is rewritten to `{"url": ...}`; Volc returned
  400 "expected an object" for the string form (cn-prod 2026-11-05 user 2).
  Inline `data:` payloads that look like a real `video/*` base64 video
  (decoded size ≥ 256 bytes), supplier schemes such as `ms://`, and URLs the
  preflight can read are still forwarded. Empty, non-video MIME, invalid
  base64, or undersized `data:` placeholders become the same local
  `invalid_parameter` / `messages.content` as an unreachable http(s) URL.
  Do not strip video parts. A body that exceeds the context window is not
  truncated.

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
URL, a sufficiently large inline `video/*` data URL and a non-Volc request keep
the video part. Undersized / non-video `data:` placeholders are local 400.
Existing Moonshot and provider
tool constraint tests must still pass. The gateway sentinel registry anchors the
supplier owner, selected-account routing and these regression tests.

发布后用隔离管理员探针核对出站请求与真实 reasoning/tool 输出；仅 HTTP 200 不足。
回滚走正常发版路径。

## Amendments（行为契约；按日 Evidence 已删）

Owner 不变：`service/volc_kimi_chat_constraints_tk.go` /
`volc_kimi_chat_request_limits_tk.go` / `normalizeVolcKimi*`；零用量计费见
[`usage-estimate-fallback-ssot.md`](usage-estimate-fallback-ssot.md)。

### Request-field / sampling / effort

- `reasoning_effort` `none|off|disabled` → `thinking.type=disabled` 并剥离 effort；
  与显式 enabled 冲突 → 400。`minimal`→`low`。其它供应保持历史 `none`→`low`。
- KVV fixed sampling（`rejectKimiK3FixedSampling`，所有 `isKimiModel` / `kimi-*`）：
  接受 omitted；`temperature` ∈ `[0,2]` 除 `1.1`/`2.0`；`top_p` 恰 0.95；
  `presence_penalty`/`frequency_penalty` 0；`n` 1。非法值 **400 不改写**。
- DashScope pin（桥接路径、较窄 id 集）：缺省 `top_p=0.95`，可将合法 temperature 钉到 1.0；
  非法值留给后续 400。
- Volc `pinVolcKimiThinkingTemperature`：thinking on → 合法非 1 钉 `1.0`；
  thinking off → 合法非 0.6 钉 `0.6`；omitted 保持 omitted。
- `n≠1`、`logprobs=true` / `top_logprobs>0` → 400；`max_tokens`/`max_completion_tokens`
  须为正整数。
- 客户端错误文案剥离尾部 `Request id: <16+ alphanumerics>`（`TkSanitizeUpstreamProductWireMessage`）；
  ops 保留原文。

### Known limitation: `stop` + thinking

thinking on 时 `stop` 可能命中 `reasoning_content` 导致空正文；网关原样转发，不删 stop、
不关 thinking。需要仅答案 stop 时客户端应 `thinking.type=disabled`。

### tool_choice / video_url / prediction / content normalize

- 非法 `tool_choice` → 字段级 400（`openai_chat_constraints_tk.go`）。
- 字符串 `video_url` → `{"url":...}`；`prediction` 须 Moonshot 形状否则字段级 400。
- 丢弃无输入 text part（`text` 为 `""` / null / 缺字段）；无 part 时 content→`""`；
  字符串 content `""` 原样转发。
- JSON null `messages.content` → `""`（Volc K3 路径；缺 key 不动）；dynamic-tool
  promotion 仍优先。

### Content-part / file / wire UX

- Drop part types：`thinking` / `reasoning_text` / `prompt_cache_breakpoint` /
  `refusal` / `summary_text`。
- Rewrite `type=video` / `input_video` → `video_url`（有 URL 时）；不剥媒体。
- `audio_url` / `input_audio` → 本地 400（不静默丢媒体）。
- Open tool round：`assistant.tool_calls` 之后、匹配 tool 结果齐前，丢弃无
  `tool_calls` 的 assistant；orphan `tool_call_id` 仍 400。
- `data:` video：须 `video/*` + base64 + 解码 ≥256B，否则本地 400；http(s) 预检
  瞬时失败最多重试 3 次（含 HEAD→ranged GET）。
- `type=file`：缺 nested `file` → 400；xor `file_id|file_url|file_data`；
  `video/*` data → `video_url`；`text/*` → text；非 PDF → 400。
- 出站 raw Chat：缺省 stream `finish_reason` 写 `null`；镜像
  `prompt_tokens_details.cached_tokens` → 外层 `cached_tokens`（不发明命中）。
- Messages→Chat：`video`/`video_url`/`input_video` → Chat `video_url`；
  `preservesMessagesToChat` 准入 ContentVideo、fail-close ContentAudio；
  `/v1/responses` 视频拒绝不变。

### Deferred

递归 `tools[].function.parameters` JSON Schema flatten 需直打 sanitize→200 证据后再做；
客户端 `: tool function parameters schema` 后缀仍仅为启发式。

按日账号/Request ID/私有探针目录与客户 PDF 回执已删；契约以上表与 Linked Tests 为准。
