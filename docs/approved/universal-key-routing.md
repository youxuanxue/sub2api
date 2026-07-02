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

## 0. TL;DR

客户痛点:为用全平台,要管一堆按平台切分的 key/分组(anthropic、openai、gemini、grok、kiro、
newapi/扩展引擎……)。要的是 **一把 key,什么都能用**。

本设计让 **key 默认就是「全能」**:全能 key 不绑死平台,**每个请求**按请求的模型 + 入口端点,
在 **key 主人有权访问的所有分组**(公开组 + 专属授权 + 生效订阅,实时计算)里解析出后端组,
再把请求 **伪装成绑定该后端组的普通 key**(替换 `apiKey.Group/GroupID`)。下游调度、计费、
粘滞、转发 **零改动**。普通(direct)key 路径 **逐字节不变**。

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
  不带分组创建→universal;带分组创建→direct,保留 PR2 开关上线前老 UI 行为)。
- 字段经 `routing_mode` 贯通到 service 结构、repo 映射/读写、**auth 快照(版本 12→13)**、
  DTO/handler、Create/Update。auth 快照携带它 → 热路径零额外 DB。

### 3.2 解析(per request)
- 形状映射 `universal_routing_tk_endpoint_map.go`:`c.FullPath()` + 方法 → `UniversalShape`,
  再 → 候选平台集合,**从 `OpenAICompatPlatforms()` / `engine.capability` 派生**(不硬编码,
  满足 compat-pool 漂移门)。
  - `/v1/messages` → `[anthropic, antigravity, gemini, kiro]`(跨度内有 messages-dispatch
    组或 Grok messages 直通组时并入 openai-compat;resolver 再按组执行
    `allow_messages_dispatch` 过滤);
    `…/count_tokens` → `[anthropic, antigravity, gemini, kiro]` + 开了 dispatch 的
    openai-compat 组(Grok 直通)。Gemini/Kiro/Antigravity 上游不暴露计数时走本地估算兜底。
  - `/v1/chat/completions`、`/v1/responses`(含 codex、无前缀别名)→ `[openai, newapi, grok]`;
    claude-* 模型额外纳入 `[anthropic]`,gemini-* 文本模型额外纳入 `[gemini]`,对齐 direct
    key 的 OpenAI-shaped 入口路由。注意 route parity 不等于所有上游都已真支持
    `/v1/responses`;实测报告必须继续区分 route-gate 与 live servability。
  - `/v1/embeddings`、`/v1/images/generations`、`/v1/video/*` → capability(`[openai, newapi]`);
    Grok 原生 image/video 模型额外纳入 `[grok]`;`/v1/images/edits` → `[openai, grok]`
    (direct handler 当前支持这两个平台;multipart 不读模型,多组并存时按 universal 确定性排序)。
  - `/v1beta/models/*` POST → `[gemini, antigravity]`;GET 元数据(含 `/v1/models`)→ 跳过。
- 解析器 `universal_routing_tk_resolver.go`:取(短 TTL 缓存的)权限跨度 `GetAvailableGroups` →
  跨度 ∩ 候选平台(active)→ **「组已服务模型集」真值过滤(见下)** → **确定性挑选(持订阅优先
  → `group.sort_order` → id)**。空 → 按入口协议形状写 403"该模型不在你的套餐内"。

  **模型/模态服务真值过滤(`universal_routing_tk_serving.go`)**:旧版仅用前缀 hint(best-effort
  偏好)选平台,对「某组账号到底服不服务这个模型」零可见 —— newapi 多 vendor 平台(deepseek/
  Qwen/volcengine/google-vertex/grok/…)里盲选必投错组 → 下游 `no available accounts`。现优先
  调用 `GatewayService.UniversalGroupSupportsModel`,复用 direct scheduler 的账号级模型支持语义;
  provider 不可判断时再退回 `GatewayService.GetAvailableModels`(与 `/v1/models` 同源)的
  服务集来源**非对称**判别:
  - 组有显式 `model_mapping`(含全部 newapi vendor 组)→ 精确成员判定 `M ∈ served`(deepseek/
    qwen/imagen/veo/seedance 落到声明该模型的组,排除别的 vendor 组);
  - 组无显式映射(native 空映射:anthropic/openai/gemini/antigravity 单一 vendor 平台的「空=透传」)
    → 前缀 hint == 组平台(单 vendor 无歧义,且匹配未进白名单的新模型);
  - 组无显式映射且平台为 newapi(多 vendor)→ **不匹配**(空映射是配置缺失,见非对称不变量)。
  收敛后非空则用收敛集,否则**退回平台级**(安全兜底:provider 取数失败/无组声明该模型时不比
  现状更严,由下游诚实拒绝;provider 经 wire 就绪钩子 `ProvideTKUniversalModelsProvider` 后期绑定,
  未接线时整体退回旧行为)。

  **非对称 model_mapping 不变量**:newapi(多 vendor)账号必须声明非空 `model_mapping`;原生单
  vendor 平台保留「空=透传」。三道闸:写时挡(`AccountService` → `ErrNewapiModelMappingRequired`)、
  路由时拒(上述 newapi-空映射不匹配)、ops 实时审计(`ops/newapi/audit-model-mapping.py`,prod-only)。
- 替换:`apiKey.Group/GroupID` = 后端组。**不设 `ForcePlatform`** —— 替换组本身就让下游按该组
  平台派生(保留 anthropic+antigravity 混合调度等普通组语义);仅 **读取** 已有 ForcePlatform
  (如 `/antigravity` 路由)把候选限制在该平台内,从不覆盖显式 force。

### 3.3 热路径缓存
`GetAvailableGroups` 一次 3 查询,逐请求不可接受。解析器内置进程级 per-user 跨度缓存
(TTL ~30s + 抖动,singleflight 合并冷重算),**稳态每请求 0 次新增 DB**;陈旧 ≤ TTL
(新授权容忍 ≤1 分钟滞后)。`Invalidate(userID)` 预留主动失效;PR1 以 TTL 兜底。

## 4. 能力与边界

**能力**:一把 key 任意客户端/端点/模型(只要被授权)直接通;自动按模型+端点找平台;
账分得清(按落地的专属组+专属倍率);新授权自动生效;粘滞/限流/冷却归因沿用各平台原有那套。

**边界**:
1. 全能 ≠ 无限,只通被授权的平台(跨度=该用户的专属分组集合)。
2. 一个请求只落一个平台,不拆分、**不跨平台 failover**。
3. 模型不在任何被授权组 → 干脆报错(不静默兜底到错平台)。
4. 同名模型撞多平台 → 模型提示偏向 + 确定性排序(可由 sort_order 调);非"自动选最便宜/最快"。
5. `count_tokens` 按模型在 Anthropic/Antigravity/Gemini/Kiro/OpenAI-compatible 授权组内收敛;
   `/v1/models` PR1 回落默认(PR3 给并集)。
6. 安全:全能 key 泄露面=该用户全部授权平台 → 默认全能宜配 key 级总额度;想锁单平台关开关。
7. images/edits 与 video 是 multipart/poll,按端点形状路由(候选≤2),不深挖 body 模型名。
8. 后台没有的能力变不出来(无视频账号的平台不会凭空有视频)。

## 5. 守卫

新 `*_tk_*.go` 热点入 `scripts/sentinels/gateway-tk.json` 锚点;端点映射从 engine 单一真值派生
(compat-pool 漂移门);`api_key_auth*.go` 单行注入为受控编辑。

## 6. 阶段

- **PR1**:后端路由内核(本文档实现)—— 全平台/全模态在此覆盖;无 UI。
- **PR2**:前端「全能」开关(默认开)+ 分组选择器联动 + api/types/i18n + 测试弹窗 flavor。
- **PR3**:全能 key 的 `/v1/models` 并集列表;模型→平台提示由前缀启发升级为 pricing 厂商真值。
