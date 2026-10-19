---
title: Plan Chat 工具与角色兼容
status: approved
approved_by: "user (2026-10-13 conversation: 请实现修复方案，指定函数仅提供该函数、none 移除 tools、阿里 developer 改 system、显式 json_object; 先把 max_tokens + max_completion_tokens 并存 → 400 修复); user (2026-10-19 conversation: 客户反馈 temperature/max_tokens 上游 400，请排查、定位、解决 — Volc Agent Plan sampling/max_tokens clamp)"
approved_at: 2026-10-19
risk: high
---

# Plan Chat 工具与角色兼容

## Intent / scope

Ali Token Plan 和 VolcEngine Agent Plan 的部分 Chat 供应路径接受工具选择参数，
却执行了其他已声明工具；Ali Token Plan 拒绝 developer。按实际选定的供应
目的地适配，适用于直连 key 和万能 key、流式和非流式。此授权涵盖请求兼容，
不发布生产、不增加能力路由策略、不改变费用或供应池。

## Contract

- 指定函数：只向供应源提供该名称的函数定义，保留原选择参数。现代 tools 和
  旧版 functions 同步过滤，避免另一入口泄漏未选工具。
- none：移除本轮 tools/functions；若同时存在两个选择字段，它们均为 none。
  不删除历史 assistant 工具调用或 tool/function 结果消息。
- auto/required/未指定选择：不缩减工具列表。此修复不保证模型必定调用工具，
  不保证 parallel_tool_calls=false 的调用数量，也不裁剪供应响应来伪造通过。
- 指定名称必须在对应工具列表中恰好定义一次；冲突选择、非法选择或缺少指定
  定义返回现有 invalid_request_error / 400，不进行供应请求，不包含客户正文。
- Ali Token Plan 的 developer 转为 system，保留消息顺序、内容和扩展字段；
  火山和其他支持 developer 的供应目的地不因模型名称相同而被改写。
- 输出长度：当存在正数 `max_completion_tokens` 时，写入 `max_tokens` 并删除
  `max_completion_tokens`，避免 Volc/Ali Plan 因双字段并存返回 400。仅
  `max_tokens`、非法或非正 `max_completion_tokens` 保持原样。预算以
  `max_completion_tokens` 为准（与 Chat 转换器优先级一致）。
- Volc Agent Plan 采样与输出上限（cn-prod `kimi-k3` InvalidParameter 证据）：
  出站将数值型 `temperature` clamp 到 `[0, 2]`、`top_p` 到 `[0, 1]`（全 Volc
  Agent Plan Chat）。`max_tokens` / 残留 `max_completion_tokens` 仅对
  `kimi-k3` 家族压到 `131072`（Agent Plan 文档最大输出 128k）；其他模型保留
  各自更高输出预算，不做 128k 全局封顶。缺省或非数值字段不动。Ali Token
  Plan 与火山 pay-as-you-go 不套用该 clamp（Ali kimi-k3 固定采样仍由既有
  Ali owner 负责）。
- 所有调整使用本次出站副本；换账号重试仍读取原始入站内容。
- response_format 完整保留。json_object 仅由调用方显式选择，客户端仍须校验
  JSON。json_schema/strict 不转换成 json_object，不剥围栏、不自行补生成结果。
  因此严格 schema 的原失败用例不会因本修复自动通过。
- 只在实际官方 Plan 目的地应用工具约束与 token 归一；代理账号名称和模型名
  不决定能力。适配由实际访问供应源的网关执行。

## Owners / flow

`openai_chat_constraints_tk.go::normalizeChatProviderRequest` 为请求约束入口，
复用 `openai_chat_roles.go::normalizeStrictChatDeveloperRoles` 处理角色，用
`coalescePlanChatTokenLimit` 归一双输出长度字段，并在 Volc Agent Plan 目的地
调用 `clampVolcPlanChatSamplingBounds`。raw Chat 和 raw Responses→Chat 在发送前
调用；NewAPI Chat、账号测试和 Responses→Chat 桥接经
`normalizeBridgeChatProviderRequest` 使用 adaptor 解析的实际目的地调用。原有
candidate/Plan 继续裁决账号与协议。

流程：选定账号/Plan → 协议转换（如需）→ 供应请求适配 → 既有传输及响应/计费。
无数据库、UI 或配置迁移（no-web-impact）。

## Validation

`backend/internal/service/openai_chat_constraints_tk_test.go`：

- `TestChatToolConstraints_SelectionAndHistory`：现代/旧工具入口、混合定义、none、
  历史保留、数值精度、strict schema 保留、幂等、入站不可变。
- `TestChatToolConstraints_InvalidAndUnconstrained`：缺失/重复定义、冲突、非法输入；
  auto/required 不变，错误不泄露内容。
- `TestChatProviderConstraints_DestinationAndRetryIsolation`：官方目的地、相似域名、
  非 Plan endpoint、模型别名、换账号隔离。
- `TestChatProviderConstraints_PlanTokenLimitCoalesce`：双字段 / 仅 completion /
  仅 max_tokens / 非法值；官方 Plan 归一，非 Plan 与 pay-as-you-go 火山不变。
- `TestChatProviderConstraints_VolcPlanSamplingBounds`：temperature/top_p
  全 Volc Plan clamp、kimi-k3 max_tokens 128k clamp、更高输出模型（如
  deepseek）保留大预算、双字段预算先 coalesce 再 clamp、合法值与非数值保留、
  幂等、入站不可变；Ali Plan 与 pay-as-you-go 火山不 clamp。
- `TestChatProviderConstraints_ForwardRawAndAliAdaptor`：真实 Ali adaptor 对本地
  HTTP 供应端、火山 raw 发送；直连/万能 key × 流式/非流式，验证出站与客户端
  工具结果、角色、双 token 字段归一、用量以及显式 json_object/strict schema 无降级。
- `TestChatProviderConstraints_InvalidSelectionDoesNotSend`：raw/bridge 返回现有
  400 错误结构，零上游调用，错误不带客户正文。
- `TestChatProviderConstraints_ResponsesFallback`：raw/bridge 协议转换后仍过滤
  指定工具或移除所有工具，流式与非流式正确转换响应、保留原始请求。

同时运行原有角色、Responses fallback、bridge、成本与缓存回归。测试使用本地
供应模拟，不代表外部模型的生成保证；上线后还需固定账号复测原始请求。
