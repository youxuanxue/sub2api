---
title: Universal API Key capability discovery
status: approved
approved_by: "xuejiao (对话审批 2026-08-18)"
approved_at: "2026-08-18"
authors: [agent]
created: 2026-08-18
related_design: docs/approved/universal-key-routing.md
related_stories: [.testing/user-stories/stories/US-046-universal-key-capability-discovery.md]
---

# Universal API Key Capability Discovery

## Goal

让默认 API Key 的产品承诺从“请求时自动路由”贯通到“发现时诚实可解释”：同一把 key 在网站和各协议客户端看到的模型，必须是它在该协议下实际可调用的模型；系统内部失败必须明确失败，不能伪装成空菜单。

## Approved Decisions

1. **默认 API Key 自动路由。** 新建 key 的普通形态是自动选择可用服务；绑定单个服务组是高级限制，不再把“Universal / 全能”作为需要理解的产品类型。
2. **一个凭证，显式协议适配。** 一把自动路由 key 可用于 OpenAI、Anthropic、Gemini、Codex 与 Antigravity 客户端，但每个发现入口按自己的协议语义返回模型子集，不提供一张混合所有协议和模态的扁平厂商墙。
3. **一个能力 SSOT。** 后端 capability owner 以用户授权组、账号级模型映射、端点形状和现有调度支持谓词为真值。网站与所有发现入口只能投影该结果，不再各自拼接授权、价目或平台 catalog。
4. **标准入口保持原生 schema。** OpenAI 入口返回 OpenAI model object，Anthropic 入口返回 Anthropic model object，Gemini 返回 Gemini `ListModelsResponse`，Codex 与 Antigravity 保留现有客户端契约。跨平台内部元数据只出现在已登录站内 API，不污染标准协议响应。
5. **列出即可调用。** 对某把 key 与某个协议列出的模型，现有 resolver 必须能确定性选择一个支持该请求形状的授权组。授权查询、能力计算或调度真值读取失败时返回错误；不得返回 HTTP 200 空列表。

## Capability Contract

能力 owner 的输入是 `user_id + api_key + protocol`，输出是稳定排序的模型能力集合。每个模型至少包含：

- `id`
- `protocols`：可调用的协议适配器
- `modalities`：由请求形状归一化得到的 chat、embedding、image、video 等能力
- `routes[]`：每个协议/模态对应的 resolver 选中组；另提供首条 route 的 `selected_group` 摘要供现有菜单渐进接入。两者仅供已认证的网站展示价格和服务归属

候选模型来自授权组实际账号的 model mapping；原生单厂商空映射账号可使用该平台的 client-facing servable catalog 作为透传候选。每个候选必须再通过 `UniversalGroupSupportsRequest`，随后交给现有 resolver 验证能选出同一授权落地组。能力 owner 不维护第二套平台、模型或优先级表。

## Projections

- `GET /v1/models`：根据明确的客户端协议识别结果投影 OpenAI 或 Anthropic 可调用子集；默认兼容形态为 OpenAI schema。
- `GET /v1beta/models`：返回该 key 可调用的 Gemini 子集与 `supportedGenerationMethods`。
- Codex `/models` 与 `/backend-api/codex/models`：先为自动路由 key 选择支持 Codex/Responses 的 OpenAI 授权组，再复用现有 manifest 获取和过滤路径。
- `GET /antigravity/models`：仅返回授权且可调用的 Antigravity 子集。
- `GET /api/v1/me/api-keys/:id/capabilities`：JWT 鉴权并校验 key ownership，返回站内菜单需要的完整能力投影；Quickstart、Studio 与后续菜单统一消费它。
- `GET /api/v1/me/pricing-catalog?api_key_id=`：direct key 保持单组价目；自动路由 key 保留在 Key picker 中并返回用户授权组价目的稳定并集、`target_group: null` 与逐模型授权组索引，界面明确显示“自动选择服务”scope，不再把无绑定组误判为 key 不存在或公共目录。

## Error And Security Semantics

- key 不属于当前用户：404，避免泄露 key 是否存在。
- direct key：只计算绑定组能力，保持限制语义。
- 自动路由 key 没有某协议能力：200 + 原生空列表，这是业务空集。
- 授权、数据库、provider 或 capability 计算失败：5xx + 现有错误 envelope，不降级为空集。
- 外部标准发现接口不返回 group ID、组名、倍率或内部平台拓扑。

## UI Contract

- 创建/编辑 key 使用“自动选择服务”作为默认模式说明。
- direct 模式说明为“限制到一个服务组”。
- Quickstart 与 Studio 按协议或模态过滤 capability API，不再用 `authorized_groups_by_model`、公共价目交集或对每组 `/v1/models` 探测来猜测自动路由 key 的能力。
- 加载失败显示可重试错误；不能把失败显示成“没有模型”。

## Non-goals

- 不在元数据请求上调用中间件 resolver 并永久锁定单一组。
- 不改变实际请求的调度、计费、粘滞或 failover 策略。
- 不承诺上游未映射、未定价或未进入 client-facing catalog 的隐藏模型可被发现。
- 不在本次改动中创建新的聚合协议或私有“万能模型”响应格式。
