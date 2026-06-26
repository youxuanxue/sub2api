---
title: 定了价才能上 — serving 准入处的运行期价格闸
status: draft
approved_by: pending
approved_at: pending
authors: [agent]
created: 2026-06-26
related_prs: []
related_commits: []
related_stories: []
related_design: docs/approved/pricing-serving-single-source-of-truth.md, docs/approved/pricing-availability-source-of-truth.md, docs/approved/channel-pricing-refund-gate-and-runtime-pricing.md, docs/approved/newapi-served-models-reconciler.md
supersedes: none
---

# 定了价才能上 — serving 准入处的运行期价格闸

> **每个模型都得有价——真价优先，没真价就落家族 floor；只有连家族 floor 都没有的，才不予服务。**
> 今天的行为是 *unpriced never blocks*：一个无可解析价格的模型照常转发、按 `$0` 记账，事后才发
> 告警——无声漏血。本设计把**价格变成 serving 的前置条件**：有真价用真价，没真价的主流家族
> （claude/gpt/gemini）**落到家族中位 floor**——**绝不 `$0`、绝不拒**有 floor 的模型；只有连家族
> floor 都没有的（多厂商 newapi/国产 的未知 id）才在准入处 `404`（backstop）。走了 floor 的请求发
> `served_at_fallback` 告警，驱动运维/自动补真价，fallback 用量随之衰减到稳态。闸**全平台默认开**
> ——安全，因为有 floor 的平台永不误拒。

## 0. TL;DR

- **堵漏**：native 平台「空 `model_mapping` = catch-all 透传」——空映射账号会服务**任意**客户
  model id，包括上游刚发、**还没真价**的 id → 按 `$0` 记账（`served_zero_cost` 只观测、从不拒绝）。
- **决策（真价 > 家族 floor > 拒）**：serving 准入处问 billing 神谕
  **`BillingService.GetModelPricing(billingKey)`**——它先查**真价**（litellm 镜像 / overlay / 渠道价），
  查不到再落 Go **家族 floor**（`getFallbackPricing`：claude / gpt / gemini-* 各有家族中位 floor）。
  - **解析出价（真价或家族 floor）→ 放行，绝不按 `$0` 服务**；
  - 走的是**家族 floor 而非真价** → 发 `served_at_fallback` 告警（收敛信号）→ 运维/自动补真价 →
    fallback 用量衰减到稳态；
  - **连家族 floor 都没有**（`ErrModelPricingUnavailable`，即多厂商 newapi / 国产 的未知 id）→
    **返回 `404`，外形与上游「模型不可用」字节对齐**（内部子码 `model_not_priced`）。
  闸用 billing 自己的两个价源（基础价含 family floor + 渠道价），不另造 catalog 影子谓词，故
  「闸 ⟺ billing」按构造成立。
- **闸全平台默认开**（`SettingKeyPricedServingGateEnabled = '*'`，迁移 `tk_047` seed `'*'`，存量
  prod 也开）。安全的原因：有 floor 的平台（claude/gpt/gemini）**永不**因缺价误拒——它们恒落 floor
  服务；只有无 floor 的 id 会被拒，而那正是我们**想要**的 backstop（避免给多厂商未知 id 大幅误计）。
  按平台移出启用集（或置空）即回滚。
- 这是 **CI-time A1 guard 的运行期对应**（`catalog-serving-drift.py`：每个 catalog/manifest id
  都可解析出价）。A1 只在 CI 保护*已上架*的 id；catch-all 路径在运行期服务的是*不在 manifest 里*
  的 id，没有任何此类检查。本设计堵的就是这个运行期缺口。
- **不是**被否决的「price ⇒ serving auto-mapping」（见 §3）。本闸**读** PRICE 与 SERVING 两个事实、
  **不拥有**任何一个：它绝不往任何账号的 serving 白名单里**加**模型，也绝不写价格；家族 floor 是
  对 PRICE 事实的**估计**，不写 `model_mapping`。

## 1. 缺口（代码佐证）

| 事实 | 佐证 | 后果 |
| --- | --- | --- |
| 空 `model_mapping` = allow-all | `Account.IsModelSupported`（`account.go:639`）：`len(mapping)==0 → return true // 无映射 = 允许所有` | native catch-all 账号（如被清空 mapping 的 Vertex 账号）会服务**任意**客户 model id，含上游新 id。 |
| unpriced never blocks | `gateway_service_tk_served_zero_cost.go`：*「计价不确定时系统选择免费放行（unpriced never blocks）…… 不拒绝服务、不改金额，纯可观测性」* | 未定价的已服务 id 被按 `$0` 记账；唯一反馈是事后的 P0 飞书告警。 |
| 价格解析会 fail-open | `billing_service.go:740`：`GetModelPricing`（litellm/overlay 真价 + Go fallback）都 miss 时返 `ErrModelPricingUnavailable`，funnel 记 `$0` 并服务。注：`channel_model_pricing` 是 billing 计费路径的**另一个**价源（`resolveChannelPricing`，`gateway_service.go:10293`），**不在** `GetModelPricing` 内——故闸必须**两个源都查**（见 §2，复审 B1）。 | 漏血窗口 = 上游发模型 → 运维注意到 P0 → 热补价，这段时间。 |
| A1 只在 CI | `pricing-serving-single-source-of-truth.md` §3：A1 断言每个 catalog/manifest id 可解析出价——**在 CI**。 | catch-all 服务的是 A1 从没见过的*非 manifest* id。运行期没有等价闸。 |
| newapi 已堵 | `account_service_tk_newapi_mapping.go`（`validateNewapiAccountModelMapping`）+ `universal_routing_tk_serving.go`（`groupServesModel`）：多 vendor 的 `newapi` 平台空映射是配置错误，写时 + 路由处都拦。 | 缺口**只在 native 单 vendor 平台**（anthropic / openai / gemini / antigravity），那里空映射是有意透传。 |

**漏洞窄而具体**：native 平台 catch-all 账号按 `$0` 服务上游新、未定价的 id。其余（newapi、已上架 manifest id）都已覆盖。

**修法 = 家族 floor，而非拒**：漏洞的根是「无价 → `$0`」，但修法**不是**把无价一律拒（那会误杀刚发布的
主流家族新模型 = 可用性回退），而是把无价的**主流家族**落到**家族中位 floor**（既非 `$0`、也不误拒），
只对**连家族 floor 都没有**的 id 才拒。floor 把「无声 `$0`」换成「响亮但偏稳的估价 + 收敛告警」。

## 2. 决策 — serving 准入处的价格闸

**不变量（这条规矩）**：每个网关请求，在 billing model id 解析后、上游转发前，若
**`BillingService.GetModelPricing(billingKey)` 返回 `ErrModelPricingUnavailable`**（即真价**与**家族
floor 都解析不出、billing 会按 `$0` 记账）**且无渠道价**，则**返回 `404`**（内部子码
`model_not_priced`）——*除非该平台未在启用集内*。**有真价或有家族 floor 的请求一律放行**，绝不 `$0`。

- **闸点 = billing 自己的两个价源，不是 catalog 影子谓词**：billing 决定记不记 `$0` 用**两个**源——
  基础价 `BillingService.GetModelPricing`（`billing_service.go:740`，litellm 镜像 / overlay 真价 +
  Go **家族 floor** 兜底）+ 渠道价 `resolveChannelPricing`（`gateway_service.go:10293`，即
  `resolver.Resolve(...).Source==PricingSourceChannel`，对应 `channel_model_pricing`）。闸用**同样两个
  源、同一个键**（native gemini/anthropic 是 `originalModel`，openai native 是 mapped `billingModel`）：
  **两个源都解析不出价（含家族 floor）才拒**。**这样「闸 ⟺ billing」按构造成立**——billing 用这两个
  源决定记不记 `$0`，闸用同样两个源、同一键，没有影子谓词可漂移（含 `getFallbackPricing` 家族 floor +
  全维度价字段 + 渠道价）。**只问 GetModelPricing 会漏渠道价 → 误拒「渠道有价、基础价缺」的可计费模型
  （复审 BLOCKER B1，已修）**。渠道价探测判的是「该渠道行**真能算出 >$0**」而非「有渠道行」——全空渠道
  行 billing 会记 `$0`（`served_zero_cost`），故仍按未定价拒，与基础价侧「全零=未定价」一致。两个源都
  走内存/既有解析，渠道价探测仅在基础价 miss 时触发（罕见），不引入热路径开销。
- **无降级金丝雀（设计转向后移除）**：早先版本在「整个 pricing 源降级」时用一个常驻 canary 探一次、
  连它都解析为未定价则放行（fail-open）。**这套机制已删**——Go **家族 floor 是硬编码、免疫 litellm/
  overlay 源故障**：一次定价文件 glitch 不会 404 掉 floored 平台（它们恒落 floor 服务），家族 floor
  **本身就是 glitch 防护**，不需要再叠一层降级 fail-open。而且无 floor 的 newapi 在缺价时**就该 reject**
  （用户决策「newapi 保留 reject」），对它 fail-open 反而违背「绝不 `$0`」。故 `tkPricingSystemDegraded`
  / canary 模型常量已一并删除。`/pricing` 与 `/v1/models` 仍用 `IsModelPriced` 做展示过滤，serving 闸
  用 billing 神谕——两面同向但闸更宽（接受家族 floor）。
- **设置开关（全平台默认开，按平台回滚）**：`SettingKeyPricedServingGateEnabled`，经
  `SettingService.IsPricedServingGateEnabled(ctx, platform)` 解析（沿用 `IsSignupBonusEnabled` 样板，
  `setting_service_tk_cold_start.go`）。它是**已启用平台集**，支持 `*` 通配 = **全平台**；**首发默认
  `'*'`（全平台一步 ON）**，移除某平台 / 置空即对该平台回滚。全平台默认安全的根据：有 floor 的平台
  （claude/gpt/gemini）永不因缺价误拒（恒落 floor），只有无 floor 的 id 被拒——而那是想要的 backstop。
  **全平台默认 `'*'` 由迁移 `tk_047_default_priced_serving_gate_all.sql` 落地**（`INSERT … ON CONFLICT
  DO NOTHING`）：缺行时 `IsPricedServingGateEnabled` 返 false（fail-open-toward-off），而进程内
  cold-start 默认只在 `InitializeDefaultSettings` 里、对已存在 DB 早返回且正常部署根本不调——故必须靠
  迁移把默认写进存量 prod，否则闸静默以「关」上线。
- **companion 文件**：闸 helper 在 service 包（`gateway_priced_serving_gate_tk.go` + 接线
  `gateway_priced_serving_gate_wiring_tk.go`），各路线在 billing model 解析后、首字节前调用；上游
  handler 文件零改动（遵守 §5 最小侵入）。
- **拒绝形（D1）**：真正的 `404`、body 按**客户端实际讲的协议**（非 `account.Platform`）字节对齐
  ——anthropic `not_found_error`、gemini `googleError`(NOT_FOUND)、openai/newapi
  `invalid_request_error`+`code:model_not_priced`，让客户端 SDK 用它既有的未知模型路径处理——**不用
  `403`**（会被 SDK 当鉴权失败 → 错误重试 + 工单噪声），**也不**无声 `$0` 成功。priced-vs-unknown 的
  区分是**运维**关切，走 body 子码 `model_not_priced` + 结构化 `priced_serving_gate.rejected` 日志
  （model、platform、api_key/group），与 `served_zero_cost` 对称——**绝不**放进客户分支用的 HTTP
  状态码里。

**为什么家族 floor，而非「无价即拒」**：纯 fail-closed（无价一律拒）优化的是*永不漏 `$0`*，代价是
拒掉每个刚发布的主流家族新模型 = 可用性回退。纯 fail-open（服务 + 告警）优化的是*永不拒*，代价是
无声漏血。家族 floor 取两者之长：主流家族新模型按**家族中位**立刻服务（不丢流量、不漏 `$0`），真价
由 `served_at_fallback` 告警几分钟内补上；只有误计风险最大的**多厂商无 floor** id 才走 reject backstop。
乔布斯式判断：尝得出大概价的（家族已知），先端上桌并立刻校准；连大概价都尝不出的（厂商都不认识），
不端。

## 3. 与既有 SSOT 设计对齐（不矛盾）

本改动**叠加**在 SSOT 设计体之上，而非与之对抗。

- **`pricing-serving-single-source-of-truth.md` —「一个事实一个 owner」**：SERVING 由 per-account
  `model_mapping` 拥有；PRICE 由 overlay + `channel_model_pricing` 拥有。**本闸不拥有任何事实**，
  它是横切的*计费完整性准入规则*——「我们不服务我们算不出钱的东西」——只**读**两个事实、**不改**
  任何一个。它绝不写 `model_mapping`，也绝不写价格。**家族 floor 是对 PRICE 事实的*估计***（Go
  硬编码兜底），不是新增的事实源、也不写回 overlay/`channel_model_pricing`——真价仍由那两个 owner
  拥有，floor 只是真价 miss 时的临时读侧估值，由 `served_at_fallback` 驱动补成真价。
- **它不是被 REJECTED 的「让白名单对齐 overlay」**：那条否决禁的是*价格存在 ⇒ 自动映射到账号*
  （#812 那种「有价看着像已服务」的幻觉：已定价但未映射 → `429/503`）。本闸是**反方向**：*价格缺失
  ⇒ 不服务（仅无 floor 时）*。它从 catch-all 会服务的集合里**减**，绝不**加**一条 serving 声明。#812
  的失败模式（已定价但未映射）不受影响——那条 id 映没映射，与本闸无关。
- **它是 A1 的运行期对应**：`catalog-serving-drift.py` 的 A1 已断言*每个 manifest/catalog id 可
  解析出价*——但只在 CI、且只覆盖已上架 id。catch-all 在运行期服务非 manifest id。本闸把**同一个
  谓词搬到运行期强制**，堵住 A1 看不见的那一面。
- **`pricing-availability-source-of-truth.md`** 已让 `/pricing` 与每个 model-list 端点只 emit
  `priced` id，明言目标*「空池暴露为 error，而非无声服务不可达模型」*。本闸把这个目标从**列表**面
  延伸到**serving**面：catalog 不**列**（连 floor 都没有）的模型，现在网关也不**服务**。
- **「上游是喂给人决策的 feed，从不是决策本身」**（§2.4 / R-002）由 §4 遵守：补价从可信源取
  *价格*、写 PRICE 事实；它**绝不**自动上架 serving（不写 `model_mapping`）。serving 仍是人/迁移的
  决策；只有价格被自动化，且只来自可信源。家族 floor 不破这条——它不来自上游、是 TK 自定的保守估值。

## 4. 让闸安全的那一半 —— 家族 floor 是可用性机制，告警驱动补真价

旧设计里「让 fail-closed 安全」靠的是「拒绝即触发补价、补完才不回退」——闸单发就会拒掉每个新模型。
**转向后这层风险已由家族 floor 从根上解决**：有 floor 的主流家族（claude/gpt/gemini）缺真价时**按 floor
服务、压根不进拒绝分支**，可用性天然不回退。补价通路因此**降级为「校准」而非「解封」**——它把按 floor
服务的请求收敛回真价，而不是把被拒的请求放出来。

### 4.1 家族 floor 本身就是可用性机制

`getFallbackPricing`（`billing_service.go:538`）按**家族**给中位 floor，真价 miss 时立即兜住：

- **gemini 家族 floor**（分 3 档，避免子档误计）：`pro` → `gemini-2.5-pro`（in `1.25e-6` / out
  `1e-5` / cacheRead `1.25e-7`）；`flash-lite` / `flash-8b` → `gemini-2.5-flash-lite`（in `1e-7` /
  out `4e-7`，**单列以免被按 flash 收 3x/6.25x 超收**——对抗复审 S3）；其余 flash / 未知 gemini →
  `gemini-2.5-flash` 中位（in `3e-7` / out `2.5e-6` / cacheRead `3e-8`）。
- **gpt 家族 catch-all（仅 chat 形）**：已知型号（gpt-5.4/5.5/mini/nano/codex…）返各自具体价；其余
  **chat 形** `gpt-*`（新变体 / 未登记 codex 后缀）→ `gpt-5.4` 中位 floor。**非 chat 形 gpt**
  （`image`/`audio`/`realtime`/`transcribe`/`tts`）**排除在 floor 外**（→ 无 floor → 走真价或被拒），
  以免按 token 中位误计成错的计费模式（对抗复审 S2）。诚实标注：premium 未知 gpt（如未镜像的 gpt-5.5，
  real out `3e-5`）会被 gpt-5.4 floor **欠收 ~2×**——欠收是临时小漏，由 `served_at_fallback` 告警驱动几
  分钟内补真价。
- **claude 家族**（既有）：opus/sonnet/haiku/fable 各档，未知 claude → sonnet-4 兜底。

floor 取**家族中位**（子档分开），不是一个 flat 价——这把「单一 flat 价误计大」的旧 C 期反对**正面解决**
（家族粒度 + 取中位），而非回避。**刻意没有 catch-all 的：newapi / 国产（deepseek/glm/kimi/minimax…）/
OpenAI o 系列 / 非 chat gpt**——它们是**白名单**语义，未命中具体型号**不回退**（→ 无 floor → 被闸拒）。
这是有意的：多厂商/跨模态价差极大，给未知 id 乱兜会大幅误计客户的钱，宁可 reject backstop。

### 4.2 `served_at_fallback` 驱动补真价（收敛引擎）

`IsServedViaFamilyFloor(model)`（`billing_service.go:728`）是收敛信号：真价 miss 但有 Go 家族 floor →
`true`。两条计费 funnel（anthropic `recordUsageCore` 在 `gateway_service.go:10201`、openai 在
`openai_gateway_service.go:6627`）在记账点都调 `tkNotifyServedAtFallback`
（`gateway_service_tk_served_zero_cost.go`），命中即发 reason `served_at_fallback` 的飞书卡片
（文案「模型按家族兜底价(floor)服务、非真价」），点名该模型去补真价。补真价后 `IsServedViaFamilyFloor`
转 `false`、告警自动停——fallback 用量衰减到稳态。它与 `served_zero_cost`（cost==0）互斥：floor 是
`cost>0`，不是漏血。

### 4.3 补价的增量阶梯（仍是【补真价】的自治度，但 gap 窗口已按 floor 服务）

阶梯仍按**来源决定自治度**（D3），但语义从「拒绝 → 解封」变成「按 floor 服务 → 校准成真价」：

- **v1（本次）= floor 服务 + `served_at_fallback` 告警 → 运维现成工具补真价。** 告警复用既有
  `PricingMissingNotifier`，运维用现成 `ops/pricing/apply-pricing-hotfix.py lookup` 取价、`apply` 经
  admin API 写渠道定价（立即生效、无发版），再 `stage-overlay` 固化进 `tk_pricing_overlay.json` 提 PR。
  人在环约 5 秒批；补价落地前模型**仍按 floor 服务**（非拒、非 `$0`）。
- **v2（fast-follow）= litellm-一键确认 + Go-native overlay runtime 写器。** 触发时把 litellm 候选
  **预填**进飞书/admin 卡，人批后由进程内 overlay runtime 写器
  （`settingRepo.Set(SettingKeyTKPricingOverlayRuntime)` + `Publish(settings_updated)`）热推、全副本即时
  reload——无发版、无 ops 脚本。litellm 是派生、偶尔出错的 feed（`$0 = 未知` 陷阱），无人值守 apply 会
  算错钱，故这一档天然需人 5 秒确认；确认期间仍按 floor 服务。
- **v3（fast-follow，需先建价源）= 官方价全自动 apply，无人无发版。** 官方价页（Vertex / OpenAI /
  Anthropic，带 `source` URL + 抓取日）是权威。**但官方价抓取（`FetchOfficialPricing`）当前两侧零实现**，
  全自动**前置依赖先建权威价源**，故是 fast-follow、非 v1 前置；在价源落地前缺真价新模型走 floor + v1
  人批通路。

**三档共有的不变量（仅写 PRICE，绝不碰 serving）：**

1. **信号**：一次 `served_at_fallback`（或对无 floor id 的闸拒绝 / `served_zero_cost`）点名一个走 floor /
   未定价、且是**候选**（在 catalog 候选集内——不是任意客户垃圾串）的模型。
2. **写入 PRICE = overlay 热推**：fill 写进 overlay（`tk_pricing_overlay.json`，git 唯一事实源），经
   **overlay runtime 热层**（`SettingKeyTKPricingOverlayRuntime`）热推 + publish `settings_updated`，所有
   副本立即 reload——运行期生效、**无发版**；runtime 只是 git 的热投影，下次例行发版折进 embedded floor。
   v1 的「写入」由运维用 `apply-pricing-hotfix.py` / `manage-overlay-runtime.py` 完成（人触发）；v2/v3
   把这一步搬进进程内。**不写 `model_mapping`**。overlay **承载全维度**（`OutputCostPerSecond` /
   `ThinkingOutputCostPerToken` 都在内，`pricing_service_tk_overlay.go`），故 veo/seedance/思考模型也无
   维度 carve-out。价格优先级不变（`channel_model_pricing` > overlay > litellm > Go 家族 floor）；家族
   floor 是该链**最底兜底**，真价一到即覆盖它。
3. **服务**：补真价后请求改用真价、`served_at_fallback` 停。**连家族 floor 都没有**的模型保持被拒
   （响亮 `404`），交人补价或弃用——绝不无声 `$0`。

## 5. 上线与铺开（全平台默认 ON，安全因有 floor，可回滚）

| 步骤 | 内容 | 行为变化 | 门槛 / 回滚 |
| --- | --- | --- | --- |
| **一步上线（全平台）** | 闸 + 家族 floor + `served_at_fallback` 告警 + v1 补价通路同时上线，启用集 = `'*'`（全平台，迁移 `tk_047` seed，存量 prod 也开） | 有 floor 的家族（claude/gpt/gemini）缺真价：**按家族 floor 服务** + 告警 → 运维约 5 秒补真价 → 改用真价；**无 floor**（newapi/国产 未知 id）：拒绝 `404`（不再 `$0` 服务） | 把某平台移出启用集（或置空 `''`）即对该平台回滚；floor 保证有 floor 的平台永不误拒，故全平台开是安全的 |

全平台默认 ON 安全的根据（与旧 gemini 首发逐平台 soak 的本质区别）：**有 floor 的平台永不因缺价误拒**
——它们恒落家族 floor 服务，最坏只是按中位估价（由告警快校准），不会拒掉真实流量。因此不需要逐平台
soak 才敢开下一个；唯一会被拒的是无 floor 的 id，那是设计**想要**的 backstop。**gemini 不再特殊**
——它和 claude/gpt 一样有家族 floor、一样默认开、一样靠 `served_at_fallback` 收敛。

站稳后，`tokenkey-servable-model-refresh` 里那套手动 catch-all「安全仪式」（probe → 补价 → soak →
清空 mapping）**退役**：机器强制*priced ⟺ servable*（priced 含 floor），人只批无 floor 那几个。
**不设 `allow_unpriced` 逃生门**——一条规矩、无 per-account 旗标（旗标是纪律的坟墓）；唯一旋钮是按
平台的启用集（含 `*` 通配），用于回滚，而非长期 bypass。

## 6. 风险与非目标

- **R1 — 可用性回退：已由家族 floor 解决。** 旧风险是「fail-closed 拒新模型」。转向后有 floor 的家族
  缺真价时**按 floor 服务、不进拒绝分支**，可用性天然不回退；闸单发也不会拒掉主流新模型。*残余*：只有
  **无 floor**（多厂商 newapi/国产 未知 id）才被拒——那是有意 backstop，且加入新厂商前可先补对应家族
  floor 或具体价。
- **R2（新主风险）— 按家族 floor 误计。** floor 是估值，可能与真价有偏差。*缓解三重夹小*：①**家族
  粒度**（不是一个 flat 价，gemini-pro / gemini-flash / gpt-5.4 各自的中位）；②**取家族中位**（不取
  上下界）；③**快补真价**（`served_at_fallback` 告警驱动，分钟级覆盖 floor）。**取值倾向中位偏稳**：
  超收比少收更伤客户信任，故 floor 在中位附近、宁稳勿冒进；偏差只在「真价补上前」的短窗存在。
- **R3（真免费模型）— 不变。** 真正免费的模型（倍率 0 的组、按策略 `$0` 的 id）不能被当「未定价」拒。
  *缓解*：闸判的是 `GetModelPricing` 能否**解析出价条目**（真价或 floor，返回非
  `ErrModelPricingUnavailable`），不是 `cost==0`。按策略定价为零但有条目的 id 仍解析得出；
  `negative_multiplier` / 免费组语义（`served_zero_cost`）不受影响。
- **R4 — 谓词漂移：按构造消除。** 闸用 billing 自己的**两个价源**（`GetModelPricing` 含 family floor +
  `resolveChannelPricing` 渠道价），与 billing 计费路径一一对应、同一键，不存在「闸谓词 vs 计费谓词」
  两套实现去漂移。无降级 canary：家族 floor 免疫源故障，本身就是 glitch 防护。回归守卫：一致性测试 +
  路线级测试 + 渠道价测试（基础价缺、渠道有价→闸放行）+ floor 收敛测试（`IsServedViaFamilyFloor`：
  gemini/gpt/claude 未知→true、无 floor 厂商→false）。
- **非目标 — 自动上架 serving**：本闸绝不写 `model_mapping`。模型变*可服务*只能走既有人/迁移路径；
  本闸只管*一个已映射/透传但无价的模型能不能过*。
- **非目标 — 让 serving 收敛到上游**：与 SSOT doc 里被否决的选项一致；上游仍是喂给人决策的 feed。
  家族 floor 也不来自上游（TK 自定保守估值）。

## 7. 机械化强制（每条规矩都有闸）

- **sentinel**（`scripts/sentinels/priced-serving-gate.json`，**13 项**）：把闸的核心
  （`tkCheckPricedServingGate` / `tkPricedServingGateRejected` / 三种 wire 协议的 404 信封字符串）、各路线
  注入点（openai / responses / chat-completions / gemini-compat / gateway 共 7 处 `tkPricedServingGate`
  调用）、设置键、迁移 `tk_047` 的 `'*'` seed、channel_handler 确认闸都钉住，让上游合并 / 重构不能无声
  删掉闸。注：sentinel rationale 已记「无降级 canary、家族 floor 即 glitch 防护」的转向。
- **preflight 测试**：R4 谓词一致性测试（`pricing_predicate_consistency_tk_test.go`，含
  `TestIsServedViaFamilyFloor`）+ 闸开/关单测（未加入启用集的平台 ⇒ 未定价照旧；启用平台 ⇒ 无 floor
  被拒 `404`、有 floor/真价照常服务）+ 告警卡测试（`served_at_fallback` 卡含「家族兜底」、不含「404
  拒绝」/「按零成本记录」）。
- **启用集测试**：断言 `*` 通配匹配所有平台、空集对所有平台放行（回滚安全）、移除某平台后该平台不受
  闸影响——与 CLAUDE.md §9.1 式「默认/未启用保持安全」守卫同形。

## 8. 决策（已定 — 乔布斯式直觉，详见正文）

- **D1 — 拒绝码 `404`，不用 `403`，且仅对无 floor**（§2 拒绝形）：上游「模型不可用」就是 `404`；`403`
  会被客户端 SDK 当鉴权失败。`404` **只发给连家族 floor 都没有的 id**——有 floor 的一律按 floor 服务、
  不拒。priced-vs-unknown 是运维关切，走 body 子码 + 日志，不进 HTTP 状态。
- **D2 — 全平台默认 ON**（§5）：不再 gemini 首发逐平台 soak。有 floor 的平台永不误拒，故 `'*'` 全平台
  开是安全的；只有无 floor id 被拒（想要的 backstop）。回滚 = 从启用集移除平台或置空。
- **D3 — 家族 floor 取家族中位**（§4.1 / R2）：floor 精确到家族（gemini-pro / gemini-flash / gpt-5.4
  各自），取**中位**而非上下界，倾向中位偏稳（超收比少收伤信任）。这把旧「flat 价误计大」反对正面解决，
  而非回避；偏差只在真价补上前的短窗。无 catch-all 的家族（newapi/国产/o 系列）刻意保持白名单→无 floor→
  reject。
- **D4 — 补真价由来源派生、分档增量落地**（§4.3）：v1 = floor 服务 + `served_at_fallback` 告警 + 运维
  现成工具补真价（人在环约 5 秒批）；v2 = litellm-一键确认 + Go-native overlay 写器；v3 = 官方价全自动
  （需先建权威价源，fast-follow 非 v1 前置）。错价比延迟更伤信任，故 litellm/官方两档天然需人或需先建源。
  非旗标、无逃生门，校准期间按 floor 服务（非拒、非 `$0`）。
- **D5 — 价格写 overlay runtime 热推**（§4.3 共有不变量 step 2）：单一事实源（git overlay）、无发版、
  承载全维度、不依赖渠道价②；`channel_model_pricing` 回归 per-channel 修正本职。家族 floor 是价格链最底
  兜底（Go 硬编码），真价一到即覆盖。v1 写入由运维工具完成（人触发同一 overlay runtime 热层）；v2/v3
  把写入搬进进程内。

## 9. 已知残留（全平台默认已覆盖有 floor 的家族；下列各面/平台逐条处理）

转向后有 floor 的家族（claude/gpt/gemini）全平台默认 ON 已闭环（floor 保证永不误拒）。下列残留**仅在
把对应面/平台真正纳入闸保护时才相关**，逐条处理，不阻断默认上线：

- **R-kiro / antigravity — 无闸 hook（enable 是 no-op）**：`AntigravityGatewayService.Forward/
  ForwardGemini`、kiro 两路 forwarder 计费但无 `tkPricedServingGate` hook。即便在启用集 `'*'` 内，闸也
  在这些路线**静默失效**（没有注入点）——把它们真正纳入闸**前需先补 hook + sentinel**。今天无害（恒按
  其平台真价/floor 走计费），但别误以为 `'*'` 已覆盖它们。
- **R-embeddings — `/v1/embeddings` 无闸**：经 `ForwardAsEmbeddingsDispatched` 计费但无
  `tkPricedServingGate`，靠事后 `served_zero_cost` 兜底（血量小）。embeddings 是未纳入面而非有意豁免
  ——纳入前补 hook。
- **R-openai o 系列 — 无家族 floor**：`getFallbackPricing` 的 gpt catch-all 只匹配含 `"gpt"` 的 id；
  OpenAI **o 系列**（o1/o3/o4 等，不含 "gpt"）**无 floor** → 缺真价时会被闸拒。gpt 主线已被 catch-all
  覆盖，o 系列是已知缺口——若要 o 系列也按 floor 服务，需为其加一档家族 floor；否则缺真价即 reject
  backstop（与多厂商无 floor 同语义）。
- **R-渠道改名键偏斜（SHOULD-FIX）**：`BillingModelSource==channel_mapped` 且渠道做了模型改名时，billing
  按 `ChannelMappedModel` 查渠道价、闸按 `originalModel` 查——若渠道价只挂 mapped 名上，闸可能误拒。native
  家族一般无 channel 级改名（`ChannelMappedModel` 空），故默认面不触；channel 型平台（newapi 等）走改名前，
  让闸键在 channel-mapped 情形也走 mapped。注：B（改 `billing_model_source` 强制人类确认）已把拨到非默认值
  的动作收敛为刻意操作。
- **R-404 契约收敛（D1，NIT）**：闸的 404 形【与真实上游对齐】（gemini `googleError` 字节同构、anthropic
  `not_found_error`、openai 404），但与网关【自身】对同一上游错误的既有处理在 anthropic/openai 上分裂
  ——anthropic 别处把上游 404 翻成 400 `invalid_request_error`「Unsupported model」（`gateway_service.go:8634`），
  openai native 404 用 `not_found_error`+`code:model_not_found`（`openai_gateway_service.go:5016`），而闸用
  404 `not_found_error`(anthropic) / `invalid_request_error`+`model_not_priced`(openai)。**不致客户端重试/鉴权误判**
  （都是 404、非 401/403/429/5xx）；要更干净时让闸 404 形与 `handleErrorResponse` 既有 404 契约收敛。
