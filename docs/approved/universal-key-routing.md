---
title: Universal Key — 一把钥匙通全平台/全模型/全模态
status: approved
approved_by: xuejiao (对话审批 2026-06-19)
approved_at: 2026-06-19
authors: [agent]
created: 2026-06-19
related_prs: []
related_stories: []
---

# Universal Key（全能 Key）

本文保留 key、授权跨度与计费绑定契约。候选资格、当前容量、空池降权与选组顺序由
[`candidate-eligibility-ssot.md`](candidate-eligibility-ssot.md) 统一规定；该契约已替代
旧版模型平台 hint 优先和支持/可用性混合判定。发现接口与站内菜单见
[`universal-key-capability-discovery.md`](universal-key-capability-discovery.md)。

## 0. TL;DR

客户痛点:为用全平台,要管一堆按平台切分的 key/分组(anthropic、openai、gemini、grok、kiro、
newapi/扩展引擎……)。要的是 **一把 key,什么都能用**。

本设计让 **key 默认就是「全能」**:全能 key 不绑死平台,**每个请求**按请求的模型 + 入口端点,
在 **key 主人有权访问的所有分组**(公开组 + 专属授权 + 生效订阅,实时计算)里解析出后端组,
再把请求 **伪装成绑定该后端组的普通 key**(替换 `apiKey.Group/GroupID`)。下游调度、计费、
粘滞、转发使用已绑定组。普通(direct)key 仍只使用绑定组。

留一个 **默认开启** 的开关:极少数想要"单平台锁定 key"的人可关掉、手选一个组(老行为完整保留)。

> 范围聚焦(Jobs 原则):
> - **做**:加一个 per-key `routing_mode` 字段 + 一个在认证内运行的解析器,把"按模型+端点选后端组"
>   这件事做成一个解析步骤,其余一切复用现有每平台管线。
> - **不做**:不重写调度器、不混池、不引入新协议入口、不改每平台分组的语义。

## 1. 计费(关键)

全能 key 背后就是 **这个用户原有的那堆专属分组**。每个请求落到它实际用的那个专属分组上,
按 **"该后端组 + 该用户对该组的专属倍率"差异化计量计费** —— Claude 请求按 Claude 组的价/倍率、
GPT 请求按 GPT 组的价/倍率,每用户还能有自己的专属倍率。

这在替换设计里 **自动成立**:替换后 `apiKey.GroupID`=后端组,计费链上
`getUserGroupRateMultiplier(user.ID, backingGroupID, …)`(`gateway_service.go`)/
`userGroupRateResolver.Resolve(...)`(`openai_gateway_service_tk_hold.go`)天然取到该用户对该
后端组的专属倍率;订阅型后端组按其订阅时窗计费。**一把 key、一个钱包**(用户余额 + key 总额度
做统一总闸),账目背后逐平台、逐用户倍率清清楚楚。**计费链零改动。**

## 2. 让它正确的一条铁律

`apiKeyAuthWithSubscription`(`internal/server/middleware/api_key_auth.go`)在 `c.Next()` 之前
就用 key 的分组决定了 **订阅计费 vs 余额计费**。全能 key `group_id=NULL` 会走余额分支、订阅存 nil;
若在 auth **之后** 才替换成订阅型后端组,handler 就会按余额扣费、绕过订阅时窗限额(静默钱漏)。

➡️ **必须在 auth 流程内部、订阅/分组校验之前就解析出后端组**(`MaybeResolveUniversal` 在
`api_key_auth.go` 与 `api_key_auth_google.go` 的用户校验之后、分组校验之前各一行调用)。之后现成的
分组可用性/权限/订阅/余额校验自然作用在已替换的后端组上。

## 3. 设计

### 3.1 数据模型
- `api_keys.routing_mode` enum `{direct(默认), universal}`(`ent/schema/api_key.go` + 迁移
  `tk_034`)。**DB 默认 `direct`**,存量 key 不被翻动;**新建 key 默认 universal**(显式值优先;
  不带分组创建→universal;带分组创建→direct)。
- 字段经 `routing_mode` 贯通到 service 结构、repo 映射/读写、**auth 快照**、
  DTO/handler、Create/Update。auth 快照携带它 → 热路径零额外 DB。

### 3.2 解析(per request)
- 形状映射 `universal_routing_tk_endpoint_map.go`:`c.FullPath()` + 方法 → `UniversalShape`,
  再 → 候选平台集合,**从 `OpenAICompatPlatforms()` / `engine.capability` 派生**(不硬编码,
  满足 compat-pool 漂移门)。
  具体平台集合与组级 opt-in 维护在该 owner，本文不复制平台清单。
  受治理的 OpenAI-shaped 文本请求扩展候选后由 `protocolrouter.Plan` 判定合法路径，
  不能用模型前缀排除合法 converter。native/media 入口保留各自的 capability owner；
  route parity 与上游 live servability 分开验证。
- 解析器 `universal_routing_tk_resolver.go` 将权限跨度与候选平台求交，剔除停用组和保留
  探测组，再委托 `evaluateGroupCandidates`。生产接线 `ProvideTKUniversalModelsProvider`
  注入 router 与 evaluator；认证阶段 `WithRequest` 将实际协议、模型、Responses path
  和请求特征交给 canonical parser，随后按 candidate 契约选组。

  共享评估区分“支持请求”和“现在可用”：有授权支持但容量暂不可用返回协议形状的 429；
  没有授权候选支持请求才返回 403；能力证据未知或取数失败不能当成无权限。
  订阅可用性、同计费层空池降权和稳定排序只维护在 candidate 契约中。

  **兼容路径**：未接 candidate evaluator 或没有模型名时，`Resolve` 仍保留旧 provider
  分支；能力发现目前也构造独立 resolver 使用 support provider。
  `universal_routing_tk_serving.go` 提供这些适配器和非治理路径的 capability helper。
  它们的 hint/服务集 fallback 不是生产候选资格规则；生产 evaluator 的错误直接返回，
  不回退到旧 provider。

  **映射配置**：NewAPI 多 vendor 账号的非空 `model_mapping` 仍由账号写入校验
  (`ErrNewapiModelMappingRequired`) 和 `ops/newapi/audit-model-mapping.py` 审计维护。
  映射存在不等于具有合法协议路径或当前容量。
- 替换:`apiKey.Group/GroupID` = 后端组。**不设 `ForcePlatform`** —— 替换组本身就让下游按该组
  平台派生(保留 anthropic+antigravity 混合调度等普通组语义);仅 **读取** 已有 ForcePlatform
  (如 `/antigravity` 路由)把候选限制在该平台内,从不覆盖显式 force。

### 3.3 热路径缓存
解析器通过进程内 per-user TTL 缓存与 singleflight 复用权限跨度；新授权受缓存过期和
`Invalidate(userID)` 影响。缓存只覆盖授权跨度，不保证每次候选评估零数据库读取。

## 4. 能力与边界

**能力**:一把 key 任意客户端/端点/模型(只要被授权)直接通;自动按模型+端点找平台;
账分得清(按落地的专属组+专属倍率);新授权自动生效;粘滞/限流/冷却归因沿用各平台原有那套。

**边界**:
1. 全能 ≠ 无限,只通被授权的平台(跨度=该用户的专属分组集合)。
2. 一个请求只落一个平台,不拆分、**不跨平台 failover**。
3. 模型不在任何被授权组 → 干脆报错(不静默兜底到错平台)。
4. 同名模型可有多个合法后端；候选排序由 candidate 契约裁决，模型平台 hint 不增加优先级。
5. `count_tokens` 按模型在 Anthropic/Antigravity/Gemini/Kiro/OpenAI-compatible 授权组内收敛;
   `/v1/models` 等发现入口使用 discovery 契约的协议投影，不永久绑定单组。
6. 安全:全能 key 泄露面=该用户全部授权平台 → 默认全能宜配 key 级总额度;想锁单平台关开关。
7. images/edits 仅读取 JSON/multipart 的 `model` 字段用于路由,不解析/复制上传内容;
   video submit 读取 JSON 模型名,poll 复用任务记录里的 submit-time 路由。
8. 后台没有的能力变不出来(无视频账号的平台不会凭空有视频)。

## 5. 守卫

新 `*_tk_*.go` 热点入 `scripts/sentinels/gateway-tk.json` 锚点;端点映射从 engine 单一真值派生
(compat-pool 漂移门);`api_key_auth*.go` 单行注入为受控编辑。

## 6. 验证

候选验收与可执行命令见
[`US-050`](../../.testing/user-stories/stories/US-050-candidate-eligibility-ssot.md)。
key 交互与能力发现的验收由 discovery 契约维护；历史 PR 阶段拆分见 git 历史。
