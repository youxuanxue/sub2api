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

> **一条规矩、全平台：解析不出价格的模型，不予服务。**
> 今天的行为恰恰相反——*unpriced never blocks*：一个无可解析价格的模型照常转发、按 `$0`
> 记账，事后才发 P0 告警。这既是无声的漏血，又把一个**没人验过价**的模型端给了付费客户。
> 本设计把**价格变成 serving 的前置条件**，在请求准入处 fail-closed（失败即拒）；并配一套
> **首见即自动定价**，让可用性不回退。**闸与自动定价同时上线、在 gemini/Vertex（漏洞重灾区）上
> 一步 ON**；其余平台经同一开关逐个铺开，可随时回滚。不发布「关着的空转闸」。

## 0. TL;DR

- **堵漏**：native 平台「空 `model_mapping` = catch-all 透传」——空映射账号会服务**任意**客户
  model id，包括上游刚发、**还没价**的 id → 按 `$0` 记账（`served_zero_cost` 只观测、从不拒绝）。
- **决策**：serving 准入处，若 **billing 自己的解析器 `GetModelPricing` 对 billing 将记账的键返
  `ErrModelPricingUnavailable`**（即会按 `$0` 记账），**返回 `404`，外形与上游「模型不可用」一致**
  （内部子码 `model_not_priced`），而非 `$0` 服务。闸用 billing 神谕、不另造 catalog 影子谓词，故
  「闸 ⟺ billing」按构造成立。闸**按平台启用**（`SettingKeyPricedServingGateEnabled`），首发启用集 =
  {gemini/Vertex}，移出即回滚。
- 这是 **CI-time A1 guard 的运行期对应**（`catalog-serving-drift.py`：每个 catalog/manifest id
  都可解析出价）。A1 只在 CI 保护*已上架*的 id；catch-all 路径在运行期服务的是*不在 manifest 里*
  的 id，没有任何此类检查。本设计堵的就是这个运行期缺口。
- **不是**被否决的「price ⇒ serving auto-mapping」（见 §3）。本闸是 fail-closed 的**减法**
  （unpriced → 不服务），**绝不**往任何账号的 serving 白名单里**加**模型。它**读** PRICE 与
  SERVING 两个事实，**不拥有**任何一个。
- **可用性靠补价通路保**（§4，**增量阶梯**）：一次闸拒绝点名一个未定价、但**在候选集内**的模型，触发
  补价通路把价**热推进 overlay runtime 层**（`SettingKeyTKPricingOverlayRuntime`，git 的
  `tk_pricing_overlay.json` 仍是唯一事实源、runtime 只是它的热投影），下一次请求即放行——运行期生效、
  **无发版**。**v1（本次）= 拒绝即触发既有缺价飞书告警，运维用现成 `apply-pricing-hotfix.py` 补价**（人
  在环、约 5 秒批）；v2 = litellm-一键确认 + Go-native overlay 写器；v3 = 官方价全自动（需先建价源，
  fast-follow）。**取不到价**的模型保持被拒（响亮 `404`），而非无声漏 `$0`。

## 1. 缺口（代码佐证）

| 事实 | 佐证 | 后果 |
| --- | --- | --- |
| 空 `model_mapping` = allow-all | `Account.IsModelSupported`（`account.go:639`）：`len(mapping)==0 → return true // 无映射 = 允许所有` | native catch-all 账号（如被清空 mapping 的 Vertex 账号）会服务**任意**客户 model id，含上游新 id。 |
| unpriced never blocks | `gateway_service_tk_served_zero_cost.go`：*「计价不确定时系统选择免费放行（unpriced never blocks）…… 不拒绝服务、不改金额，纯可观测性」* | 未定价的已服务 id 被按 `$0` 记账；唯一反馈是事后的 P0 飞书告警。 |
| 价格解析会 fail-open | `billing_service.go:744`：`GetModelPricing` 在动态价（litellm/overlay/`channel_model_pricing`）与 fallback 都 miss 时返 `ErrModelPricingUnavailable`，funnel 记 `$0` 并服务。 | 漏血窗口 = 上游发模型 → 运维注意到 P0 → 热补价，这段时间。 |
| A1 只在 CI | `pricing-serving-single-source-of-truth.md` §3：A1 断言每个 catalog/manifest id 可解析出价——**在 CI**。 | catch-all 服务的是 A1 从没见过的*非 manifest* id。运行期没有等价闸。 |
| newapi 已堵 | `account_service_tk_newapi_mapping.go`（`validateNewapiAccountModelMapping`）+ `universal_routing_tk_serving.go`（`groupServesModel`）：多 vendor 的 `newapi` 平台空映射是配置错误，写时 + 路由处都拦。 | 缺口**只在 native 单 vendor 平台**（anthropic / openai / gemini / antigravity），那里空映射是有意透传。 |

**漏洞窄而具体**：native 平台 catch-all 账号按 `$0` 服务上游新、未定价的 id。其余（newapi、已上架 manifest id）都已覆盖。

## 2. 决策 — serving 准入处的价格闸

**不变量（这条规矩）**：每个网关请求，在 billing model id 解析后、上游转发前，若
**`BillingService.GetModelPricing(billingKey)` 返回 `ErrModelPricingUnavailable`**（即 billing 会按
`$0` 记账），则**返回 `404`**（内部子码 `model_not_priced`）——*除非该平台未在启用集内*。

- **闸点 = billing 神谕，不是 catalog 影子谓词**：闸**直接调 billing 自己的解析器**
  `BillingService.GetModelPricing`（`billing_service.go:700`），用 **billing 将记账的同一个键**
  （native gemini/anthropic 是 `originalModel`，openai native 是 mapped `billingModel`），`!errors.Is(err, ErrModelPricingUnavailable)`
  才放行。**这样「闸 ⟺ billing」按构造成立**：billing 用这个调用决定记不记 `$0`，闸用同一调用同一键，
  没有 catalog 影子谓词可漂移（含 `getFallbackPricing` family 兜底 + 全维度价字段）。GetModelPricing 走
  内存价源（litellm 镜像 / overlay / fallback 都在内存），不引入额外 I/O。**降级 fail-open**：若整个
  pricing 源降级，先用常驻已定价 canary 探一次，连它都解析为未定价则判降级 → 放行（与 billing 降级
  fail-open `$0` 同向，不把定价文件 glitch 放大成整服务 404）。`/pricing` 与 `/v1/models` 仍用
  `IsModelPriced` 做展示过滤，serving 闸用 billing 神谕——两面同向但闸更严（要可解析出真价）。
- **设置开关（按平台启用，回滚 + 灰度）**：`SettingKeyPricedServingGateEnabled`，经
  `SettingService.IsPricedServingGateEnabled(ctx, platform)` 解析（沿用 `IsSignupBonusEnabled` 样板，
  `setting_service_tk_cold_start.go:84`）。它是**已启用平台集**：首发 = {gemini/Vertex}（一步 ON），
  其余平台加入即生效、移出即回滚。未加入的平台 serving 照旧——这是回滚/灰度旋钮，不是「默认全关空转」。
- **companion 文件**：一个 `*_tk_*.go` 准入 helper（如 `gateway_handler_tk_priced_serving_gate.go`），
  从网关入口调用；上游 handler 只多一行 import + 一行 guard 调用（遵守 §5 最小侵入）。
- **拒绝形（D1）**：真正的 `404`、body 按上游「模型不可用」字节对齐，让客户端 SDK 用它既有的
  未知模型路径处理——**不用 `403`**（会被 SDK 当鉴权失败 → 错误重试 + 工单噪声），**也不**无声
  `$0` 成功。priced-vs-unknown 的区分是**运维**关切，走 body 子码 `model_not_priced` + 结构化
  `priced_serving_gate.rejected` 日志（model、platform、api_key/group），与 `served_zero_cost`
  对称——**绝不**放进客户分支用的 HTTP 状态码里。

**为什么 fail-closed，而非「服务 + 告警」**：「服务 + 告警」优化的是*永不拒绝请求*，代价是
（a）无声漏血、（b）把没验过的模型端给付费客户。乔布斯式判断：没定价/没尝过的，不端上桌。
fail-closed 的代价——拒掉一个刚发布的模型——由 §4 自动定价**消除**，而非承受。

## 3. 与既有 SSOT 设计对齐（不矛盾）

本改动**叠加**在 SSOT 设计体之上，而非与之对抗。

- **`pricing-serving-single-source-of-truth.md` —「一个事实一个 owner」**：SERVING 由 per-account
  `model_mapping` 拥有；PRICE 由 overlay + `channel_model_pricing` 拥有。**本闸不拥有任何事实**，
  它是横切的*计费完整性准入规则*——「我们不服务我们算不出钱的东西」——只**读**两个事实、**不改**
  任何一个。它绝不写 `model_mapping`，也绝不写价格。
- **它不是被 REJECTED 的「让白名单对齐 overlay」**：那条否决禁的是*价格存在 ⇒ 自动映射到账号*
  （#812 那种「有价看着像已服务」的幻觉：已定价但未映射 → `429/503`）。本闸是**反方向**：*价格缺失
  ⇒ 不服务*。它从 catch-all 会服务的集合里**减**，绝不**加**一条 serving 声明。#812 的失败模式
  （已定价但未映射）不受影响——那条 id 映没映射，与本闸无关。
- **它是 A1 的运行期对应**：`catalog-serving-drift.py` 的 A1 已断言*每个 manifest/catalog id 可
  解析出价*——但只在 CI、且只覆盖已上架 id。catch-all 在运行期服务非 manifest id。本闸把**同一个
  谓词搬到运行期强制**，堵住 A1 看不见的那一面。
- **`pricing-availability-source-of-truth.md`** 已让 `/pricing` 与每个 model-list 端点只 emit
  `priced` id，明言目标*「空池暴露为 error，而非无声服务不可达模型」*。本闸把这个目标从**列表**面
  延伸到**serving**面：catalog 不**列**（未定价）的模型，现在网关也不**服务**。同一谓词、两个面——
  「列得出 ⟺ 服务得了」成立。
- **「上游是喂给人决策的 feed，从不是决策本身」**（§2.4 / R-002）由 §4 遵守：自动定价从可信源取
  *价格*、写 PRICE 事实；它**绝不**自动上架 serving（不写 `model_mapping`）。serving 仍是人/迁移的
  决策；只有价格被自动化，且只来自可信源。

## 4. 让 fail-closed 安全的那一半（自动定价的增量阶梯）

没有「补价通路」的 fail-closed = 「拒掉每个刚发布的模型」= 可用性回退。所以闸**绝不单独发布**：它
必须配一条真实的补价通路，让拒绝是**临时**的、几分钟内由补价解除（R4，见下）。这条通路按**三档增量**
落地——能力是*派生*的、按来源决定自治度（D3），先发最简的、把全自动留作 fast-follow：

- **v1（本次落地）= 闸 + 拒绝即触发既有缺价告警 → 运维现成工具补价。** 一次闸拒绝复用既有
  `PricingMissingNotifier`（与 `served_zero_cost` / `PricingMissing` 同一信号面，
  `gateway_service_tk_served_zero_cost.go:106` + `pricing_missing_notifier_tk.go`）即时发一张飞书卡
  点名「模型 X 未定价被拒、去补价」（reason `gate_rejected_unpriced`，带 requested model + platform）。
  运维用**现成** `ops/pricing/apply-pricing-hotfix.py lookup` 取价、`apply` 经 admin API 写渠道定价
  （立即生效、无发版），再 `stage-overlay` 固化进 `tk_pricing_overlay.json` 提 PR。这是**人在环、约
  5 秒批**的补价通路——不是全自动，但**真实存在**，足以让闸非空转（R4）。等补价落地前模型仍是 `404`，
  不破例服务。
- **v2（fast-follow）= litellm-一键确认 + Go-native overlay runtime 写器。** 触发时把
  `apply-pricing-hotfix.py lookup` 的 litellm 候选**预填**进飞书/admin 卡，人批后由进程内 overlay
  runtime 写器（`settingRepo.Set(SettingKeyTKPricingOverlayRuntime)` + `Publish(settings_updated)`，复
  用既有 reload 链）热推、全副本即时 reload——**无发版、无 ops 脚本**。litellm 是派生、偶尔出错的 feed
  （`$0 = 未知` 陷阱），无人值守 apply 会算错客户的钱，故这一档**天然需人 5 秒确认**；确认期间仍 `404`。
- **v3（fast-follow，需先建价源）= 官方价全自动 apply，无人无发版。** 官方价页（Vertex / OpenAI /
  Anthropic，带 `source` URL + 抓取日）是权威，让人盖章是官僚剧场——这是「上游发完几分钟模型就能用」
  的魔法。**但官方价抓取（`FetchOfficialPricing`）与候选集谓词当前两侧零实现**，全自动**前置依赖先建
  权威价源**，故它是 fast-follow、**非 v1 前置**；在价源落地前，gemini/Vertex 缺价新模型走 v1 的人批
  通路（litellm 对主流新模型覆盖快），不会退化成纯拒绝。

**三档共有的不变量（仅写 PRICE，绝不碰 serving）：**

1. **信号**：一次闸拒绝（或 `served_zero_cost` / `PricingMissing`）点名一个未定价、且是**候选**（在
   catalog 候选集内——不是任意客户垃圾串）的模型。
2. **写入 PRICE = overlay 热推**：fill 写进 overlay（`tk_pricing_overlay.json`，git 唯一事实源），经
   **overlay runtime 热层**（`SettingKeyTKPricingOverlayRuntime`）热推 + publish `settings_updated`，所有
   副本立即 reload——运行期生效、**无发版**；runtime 只是 git 的热投影，下次例行发版折进 embedded floor。
   v1 的「写入」由运维用 `apply-pricing-hotfix.py` / `manage-overlay-runtime.py` 完成（人触发）；v2/v3
   把这一步搬进进程内（自动/半自动）。**不写 `model_mapping`**。overlay **承载全维度**
   （`OutputCostPerSecond` / `ThinkingOutputCostPerToken` 都在内，`pricing_service_tk_overlay.go`），故
   veo/seedance/思考模型也无维度 carve-out。价格优先级不变（`channel_model_pricing` > overlay >
   litellm > Go fallback）；`channel_model_pricing` 与 `channel-pricing-refund-gate-and-runtime-pricing.md`
   的「②」是 per-channel 价的**正交轨道**，本设计**不依赖②落地**。
3. **服务**：下一次请求解析出价 → 过闸。**完全取不到价**的模型保持被拒（响亮 `404`），交人补价或弃用
   ——绝不无声 `$0`。

## 5. 上线与铺开（gemini/Vertex 一步 ON，逐平台扩，可回滚）

| 步骤 | 内容 | 行为变化 | 门槛 / 回滚 |
| --- | --- | --- | --- |
| **一步上线** | 闸 + v1 补价通路（拒绝即告警 + 现成补价工具）同时上线，启用集 = {gemini/Vertex}（catch-all 重灾区） | gemini/Vertex 未定价 id：拒绝 `404` + 飞书告警 → 运维约 5 秒补价 → 下次放行；取不到价 → 保持 `404`（不再 `$0` 服务） | 把 gemini 移出启用集即回滚；v1 补价通路人在环填真缺口、无错源价 |
| **逐平台扩** | 把其余 native 平台（anthropic/openai/antigravity）加入启用集，每个 soak 后再加下一个 | 该平台未定价 id 同上 | 逐平台：该平台 `served_zero_cost` 在 soak 窗口读 ~0、补价通路干净落地 |

gemini/Vertex 站稳后，`tokenkey-servable-model-refresh` 里那套手动 catch-all「安全仪式」（probe →
补价 → soak → 清空 mapping）**退役**：机器强制*priced ⟺ servable*，人只批自动取价拿不到的那几个。
**不设 `allow_unpriced` 逃生门**——一条规矩、无 per-account 旗标（旗标是纪律的坟墓）；唯一旋钮是
按平台的启用集，用于灰度与回滚，而非长期 bypass。

## 6. 风险与非目标

- **R1 — 可用性回退**：fail-closed 若没有补价通路 = 拒新模型。*缓解*：闸与**补价通路**（v1 = 拒绝即
  告警 + 现成 `apply-pricing-hotfix.py`，人在环约 5 秒批；v2/v3 逐步自动化，§4 阶梯）**同时**上线（绝不
  单发「空转闸」，R4），首发只在 gemini/Vertex；其余平台逐个加入、每个 soak；移出启用集即回滚。v1 非
  全自动，但补价通路真实存在、分钟级解除拒绝——满足「闸非空转」。
- **R2 — 真免费模型**：真正免费的模型（倍率 0 的组、按策略 `$0` 的 id）不能被当「未定价」拒掉。
  *缓解*：闸判的是 `GetModelPricing` 能否**解析出价条目**（返回非 `ErrModelPricingUnavailable`），不是
  `cost==0`。按策略定价为零但有条目的 id 仍解析得出；`negative_multiplier` / 免费组语义
  （`served_zero_cost`）不受影响。
- **R3 — 谓词漂移：按构造消除**：闸**就是** billing 的 `GetModelPricing`（同一调用、同一键），不存在
  「闸谓词 vs 计费谓词」两套实现去漂移——这是把闸从早先的 catalog 影子谓词改成 billing 神谕的根本收益
  （评审 BLOCKER1 + fallback/字段缺口都源于影子谓词,一并消失）。仍加一条一致性测试 + 路线级测试（catch-all
  下 requested≠mapped、requested 未定价→闸按 billing 键拒）作回归守卫,并实证「改坏闸键→测试变红」。
- **非目标 — 自动上架 serving**：本闸绝不写 `model_mapping`。模型变*可服务*只能走既有人/迁移路径；
  本闸只管*一个已映射/透传但无价的模型能不能过*。
- **非目标 — 让 serving 收敛到上游**：与 SSOT doc 里被否决的选项一致；上游仍是喂给人决策的 feed。

## 7. 机械化强制（每条规矩都有闸）

- **sentinel**（`scripts/sentinels/*.json`）：把闸的调用点 + 准入 helper 里的 `IsModelPriced` 用法
  钉住，让上游合并 / 重构不能无声删掉闸。
- **preflight 测试**：R3 谓词一致性测试 + 闸开/关单测（未加入启用集的平台 ⇒ 未定价照旧服务；启用
  平台 ⇒ 未定价被拒 `404`、已定价照常服务）。
- **启用集测试**：断言未在启用集内的平台不受影响（serving 照旧），且 gemini/Vertex 在首发启用集内——
  与 CLAUDE.md §9.1 式「默认/未启用保持安全」守卫同形。

## 8. 决策（已定 — 乔布斯式直觉，详见正文）

四个决策都用「用户体验到什么？」框定——三个有显然答案，只有 D3 是真品味判断。

- **D1 — 拒绝码 `404`，不用 `403`**（§2 拒绝形）：上游「模型不可用」就是 `404`；`403` 会被客户端
  SDK 当鉴权失败。priced-vs-unknown 是运维关切，走 body 子码 + 日志，不进 HTTP 状态。
- **D2 — 首发启用集 = gemini/Vertex**（§5）：火在这儿（catch-all、手动仪式、最高新模型节奏、官方价
  清晰），爆炸半径单平台、可回滚。
- **D3 — 自动定价由来源派生、分档增量落地**（§4）：v1 = 闸 + 拒绝即触发既有缺价告警，运维用现成
  `apply-pricing-hotfix.py` 补价（人在环、约 5 秒批，**真实补价通路、非全自动**）；v2 = litellm-一键确认
  + Go-native overlay 写器（半自动）；v3 = 官方价全自动（需先建权威价源，**fast-follow 非 v1 前置**）。
  错价比延迟更伤信任，故 litellm/官方两档天然需人或需先建源。非旗标、无逃生门，等确认期间仍 `404`。
- **D4 — 价格写 overlay runtime 热推**（§4 共有不变量 step 2）：单一事实源（git overlay）、无发版、
  承载全维度、不依赖②；`channel_model_pricing` 回归 per-channel 修正本职。v1 的写入由运维工具完成（人
  触发同一 overlay runtime 热层）；v2/v3 把写入搬进进程内（Go-native overlay 写器），事实源与热推链不变。
