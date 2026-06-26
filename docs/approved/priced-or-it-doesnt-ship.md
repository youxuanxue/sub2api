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
> **首见即自动定价**，让可用性不回退。默认 OFF 上线，待自动定价那一半就绪并 soak 后，再按平台翻默认。

## 0. TL;DR

- **堵漏**：native 平台「空 `model_mapping` = catch-all 透传」——空映射账号会服务**任意**客户
  model id，包括上游刚发、**还没价**的 id → 按 `$0` 记账（`served_zero_cost` 只观测、从不拒绝）。
- **决策**：serving 准入处，若 billing model **无可解析价**（`!IsModelPriced`），**返回 `404`，
  外形与上游「模型不可用」一致**（内部子码 `model_not_priced`），而非 `$0` 服务。闸**受设置开关、
  默认 OFF**（`SettingKeyPricedServingGateEnabled`），故 P0 零行为变化。
- 这是 **CI-time A1 guard 的运行期对应**（`catalog-serving-drift.py`：每个 catalog/manifest id
  都可解析出价）。A1 只在 CI 保护*已上架*的 id；catch-all 路径在运行期服务的是*不在 manifest 里*
  的 id，没有任何此类检查。本设计堵的就是这个运行期缺口。
- **不是**被否决的「price ⇒ serving auto-mapping」（见 §3）。本闸是 fail-closed 的**减法**
  （unpriced → 不服务），**绝不**往任何账号的 serving 白名单里**加**模型。它**读** PRICE 与
  SERVING 两个事实，**不拥有**任何一个。
- **可用性靠自动定价保**（§4）：首次请求一个未定价、但**在候选集内**的模型，会触发从可信源
  （官方价页 / litellm 镜像）取价，写入**仅 PRICE owner**（`channel_model_pricing` / overlay，
  对齐「② runtime pricing」轨道），下一次请求即放行——分钟级、无人工、无发版。**取不到价**的模型
  保持被拒（响亮的 `404`），而非无声漏 `$0`。

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
`!PricingCatalogService.IsModelPriced(billingModel, platform)`，则**返回 `404`**（内部子码
`model_not_priced`）——*除非闸被关闭*（默认）。

- **闸点**：请求准入处，复用既有价格谓词 `PricingCatalogService.IsModelPriced(modelID, platform)`
  （`pricing_catalog_membership_tk.go:51`），它已是 catalog / `/v1/models` 的过滤器
  （`model_list_filter_tk.go:48`）。同一个谓词，现在也在 *serving* 路径强制——于是「列得出来」
  与「服务得了」终于一致。
- **设置开关**：`SettingKeyPricedServingGateEnabled`，经 `SettingService.IsPricedServingGateEnabled(ctx)`
  解析（沿用 `IsSignupBonusEnabled` 样板，`setting_service_tk_cold_start.go:84`）。**默认 `false`**
  → P0 零行为变化。
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

## 4. 首见即自动定价（phase 2 — 让 fail-closed 安全的那一半）

没有它的 fail-closed = 「拒掉每个刚发布的模型」= 可用性回退。有了它，闸唯一可见的效果是：某个
全新模型的*第一个*请求可能在价格落地前的几分钟内被拒。

**管道（仅写 PRICE owner，绝不碰 serving）：**

1. **信号**：一次闸拒绝（或既有的 `served_zero_cost` / `PricingMissing` 信号）点名一个未定价、且是
   **候选**（在 catalog 候选集内——不是任意客户垃圾串）的模型。
2. **取价 + 按来源定自治档（D3，禁臆造）**：解析价格，并**让来源决定自治度**——档位是*派生*的、
   绝非运维旗标：
   - **官方价页**（Vertex / OpenAI / Anthropic，带 `source` URL + 抓取日）→ **全自动 apply**，无人、
     无发版。让人给权威价盖章是官僚剧场；这正是「上游发完几分钟模型就能用」的魔法。
   - **只有 litellm 镜像价**（找不到官方源）→ **不自动 apply**。litellm 是派生、偶尔出错的 feed
     （它的 `$0 = 未知` 陷阱）；无人值守 apply 会算错客户的钱，而算错价对信任的破坏远大于几分钟延迟。
     推一张**一键确认**（飞书卡片 / admin 操作），价格已预填——人花 5 秒批，而非 30 分钟查。

   这正是 `apply-pricing-hotfix.py` runbook 已编码的来源契约；phase 2 把它接到信号上，并按来源分
   「全自动 vs 一键确认」。
3. **写入 PRICE owner**：写 `channel_model_pricing`（运行期、无发版——「② runtime pricing」轨道），
   或固化 overlay fill。**不写 `model_mapping`**。价格优先级不变（`channel_model_pricing` > overlay
   > litellm > Go fallback）。
4. **服务**：下一次请求解析出价 → 过闸。**完全取不到价**的模型保持被拒（响亮 `404`），交人补价或
   弃用——绝不无声 `$0`。

**写路范围（D4）— P1 不阻塞在整个②的构建上**：`channel_model_pricing` *今天*已能承载
`token / per_request / image` 价（`channel.go:75`）——覆盖 gemini chat + imagen，即 §5 首选平台
gemini/Vertex catch-all 漏血的主体。所以 P1 自动定价**现在**就把这些维度写进 `channel_model_pricing`
（运行期、无发版），不依赖②。channel writer 还扛不动的维度——`video` / per-second / 思考档
（veo、seedance、思考模型）——暂留**人工经 overlay 上架**，等②补齐 resolver 写路；对它们，闸只是把
未定价 id 一直拦着直到有人补价。②落地后，自动定价免费扩展到这些维度。（overlay 是 `//go:embed` 的
→ 改价要发版才生效，故它是尚未运行期化维度的*兜底*，绝非自动定价的热路径。）

**对齐而非重复**：这是 `channel-pricing-refund-gate-and-runtime-pricing.md` 里已 staged 的「②
runtime pricing」的**需求驱动触发器**。那里的 refund gate / validator 不变量在这里变成 load-bearing。
phase 2 不发明新的价格 writer，它触发那个已规划的。

## 5. 分阶段与灰度（每阶段各自独立安全）

| 阶段 | 落地内容 | 行为变化 | 进入下一阶段的门槛 |
| --- | --- | --- | --- |
| **P0** | `SettingKeyPricedServingGateEnabled`（默认 **false**）+ 准入闸 companion + 拒绝形 + 结构化日志 + 测试 + sentinel | **无**（闸关） | 闸代码 review 过；`served_zero_cost` 基线采到 |
| **P1** | 自动定价触发器接到未定价信号（§4）；价格来源可信度契约强制 | 对 serving 无变化；价格开始自动落地 | 观测到自动定价分钟级填上真缺口；无错源价 |
| **P2** | **按平台、gemini/Vertex 先（D2）**——catch-all 重灾区——翻默认，再铺其余，各自 soak | 未定价 id 从 `$0` 服务改为被拒 | 逐平台：该平台的 `served_zero_cost` 在 soak 窗口读 ~0 |

P2 按平台、可回滚（把设置翻回去）。`tokenkey-servable-model-refresh` 里那套手动 catch-all「安全
仪式」（probe → 补价 → soak → 清空 mapping）在 P2 稳定后**退役**：机器强制*priced ⟺ servable*，人
只批自动取价拿不到的那几个。**不设 `allow_unpriced` 逃生门**——一条规矩、无 per-account 旗标（旗标
是纪律的坟墓）；唯一旋钮是全局、按平台的设置，用于灰度，而非长期 bypass。

## 6. 风险与非目标

- **R1 — 可用性回退**：自动定价之前的 fail-closed = 拒新模型。*缓解*：P0 默认 OFF；默认只在 P1
  （自动定价）上线、且 P2 按平台 soak 后才翻。
- **R2 — 真免费模型**：真正免费的模型（倍率 0 的组、按策略 `$0` 的 id）不能被当「未定价」拒掉。
  *缓解*：闸判的是 `IsModelPriced`（*价条目存在*），不是 `cost==0`。按策略定价为零的 id 仍有条目；
  `negative_multiplier` / 免费组语义（`served_zero_cost`）不受影响。
- **R3 — 谓词漂移**：若 `IsModelPriced` 与真正的计费 resolver（`GetModelPricing`）不一致，闸可能
  放过一个随后按 `$0` 记账的模型（或拒掉一个本可定价的）。*缓解*：加测试断言候选集上
  `IsModelPriced(m) ⟺ GetModelPricing(m) != ErrModelPricingUnavailable`；两者本已共享 catalog
  解析（`pricing_catalog_supported_models_tk.go:230`）。
- **非目标 — 自动上架 serving**：本闸绝不写 `model_mapping`。模型变*可服务*只能走既有人/迁移路径；
  本闸只管*一个已映射/透传但无价的模型能不能过*。
- **非目标 — 让 serving 收敛到上游**：与 SSOT doc 里被否决的选项一致；上游仍是喂给人决策的 feed。

## 7. 机械化强制（每条规矩都有闸）

- **sentinel**（`scripts/sentinels/*.json`）：把闸的调用点 + 准入 helper 里的 `IsModelPriced` 用法
  钉住，让上游合并 / 重构不能无声删掉闸。
- **preflight 测试**：R3 谓词一致性测试 + 闸开/关单测（闸关 ⇒ 未定价照旧服务；闸开 ⇒ 未定价被拒
  `404`、已定价照常服务）。
- **设置默认测试**：断言 `SettingKeyPricedServingGateEnabled` cold-start 默认为 `false`（P0 安全
  不变量），与 §9.1 式「默认保持安全」守卫同形。

## 8. 决策（已定 — 乔布斯式直觉）

此前四个 open question 已定。每个都用「用户体验到什么？」重新框定——三个有显然答案，只有 D3 是真
品味判断。

- **D1 — 拒绝码 `404`，不用 `403`**：TK 是上游 API 的 drop-in；它们的「模型不可用」就是
  `404 model_not_found`。`403` 会被客户端 SDK 当鉴权/权限失败 → 错误重试 + 工单噪声。priced-vs-unknown
  的区分是**运维**关切 → 走 body 子码 `model_not_priced` + 结构化日志，绝不进客户分支用的 HTTP 状态。
  （重新框定：「和 404 区分开」是错的目标——客户不需要区分；运维需要，且能从带外拿到。）
- **D2 — 首个翻默认平台：gemini/Vertex**：火在这儿——空映射 catch-all、那套手动安全仪式、最高的新
  模型节奏（imagen/veo/gemini-N）且官方价页清晰。爆炸半径单平台、可回滚。先在这证明，再铺其余。
- **D3 — 自动定价自治度：按来源分两档（唯一的真判断）**：官方价 → **全自动**（给权威价盖章是剧场）；
  只有 litellm 镜像价 → **一键人确认**（价格预填），因为无人值守的错价会算错客户的钱，而错价对信任的
  破坏远大于几分钟延迟。档位*由来源派生*、非运维旗标——一条规矩、无逃生门。
- **D4 — P1 价格 writer：现在写 `channel_model_pricing` 的 token/image；不阻塞在整个②上**：channel
  writer 已承载 `token / per_request / image`——足够覆盖 gemini chat + imagen，即 D2 重灾区的主体——
  运行期、无发版。`video` / per-second / 思考维度等②，暂留人工 overlay 上架；闸只是把那些未定价 id
  拦着直到补价。P1 对齐②，但不依赖②完工。

这些决策让闸**能针对 gemini/Vertex 重灾区直接上线、且绝大多数自动定价**——这正是整个设计的意义。
