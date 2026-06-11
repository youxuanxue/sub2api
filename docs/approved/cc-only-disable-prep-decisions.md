---
title: 关闭 cc-only 的代码准备（cc-only-disable-prep）
status: approved
approved_by: jobs (architect decision doc)
approved_at: 2026-06-10
authors: [agent]
created: 2026-06-10
related_prs: []
related_commits: []
---

# 决策文档：关闭 cc-only 的代码准备（cc-only-disable-prep）

> 注：本目录其它文档无显式 R1–R5 不变量模板约束，本文件按**普通权威设计文档**撰写，供高级工程师直接照做。
> 范围：**只做代码准备**，让"关闭 anthropic 账号 cc-only 限制"在代码上安全可行。**绝不**改线上 DB（cc_only flag）、绝不发版、绝不 deploy、绝不 push/PR（除非另获授权）。
> 决策人视角：聚焦 / 简洁 / 端到端体验 / 设计即工作方式 / 精品意识。

---

## 0. 一句话结论

> ⚠️ **方向已修订（2026-06-11）——本节及 D1–D4 的「单开关 / 入口拒绝」结论已被文末「[方向修订（D1/D4）](#方向修订d1d42026-06-11运营授权)」一节覆盖，以那一节为准。** 下面保留为决策演进的审计记录：原方向是「在入口拒掉非 CC」（单开关），实际落地方向是「放行非 CC + canonical TLS 出口兜底转换」，并把单开关拆成两个正交开关（入口拒绝 / haiku 出口补全）。读本文请先读文末修订节再回看 D1–D4 的论证。

关 cc-only 后，**canonical OAuth 入口 UA 门禁是唯一的客户端身份闸**。它今天是 deny-list（空 UA / 未知 UA 全放行），这对"裸奔的 edge"是危险的。但**把它无条件改成 allow-list（拒空 UA）会误杀真实匿名客户端**——因为 prod→edge 是**透明 apikey 透传，客户端 UA 原样穿过**（已用代码证实，见 §1），空 UA 是真实客户端信号而非中继伪影。因此最优解不是"一刀切收紧门禁"，而是：**新增一个默认关闭的 admin setting，把门禁从 deny-list 升级为 allow-list（含拒空 UA），并把这套收紧行为整体置于该 setting 之后**。未开启 = 完全保持当前 upstream 行为（零回归）；运营在关某个组的 cc_only 之前，先在目标 edge 开这个 setting 做单边 canary，秒级可回滚。

---

## 1. 已用代码证实的关键事实（D1 的地基）

| 事实 | 证据锚点 |
|---|---|
| prod 的 anthropic 账号全是 apikey 镜像中继；真实 OAuth 在 edge | 背景已核实；本仓 `forwardAnthropicAPIKeyPassthrough` / `buildUpstreamRequestAnthropicAPIKeyPassthrough` 即 prod→edge 路径 |
| **prod→edge 透传把客户端 UA 原样转发，无任何回填** | `buildUpstreamRequestAnthropicAPIKeyPassthrough`（`gateway_service.go:5865`）逐 header 复制，`allowedHeaders["user-agent"]==true`（`gateway_service.go:419`）；只 Del 了 authorization/x-api-key/cookie，**未触碰 User-Agent**。空 UA 透传后仍是空 UA。 |
| **没有可识别"这是 prod→edge 中继跳"的内部 header** | 透传路径只重注入 `x-api-key`，未加 `x-tk-relay` / internal marker。grep 无 relay/mirror 专用 ingress 信号。⇒ D1 不能依赖一个不存在的"可信中继"判定。 |
| 入口 UA 门禁仅对 canonical OAuth 生效，且只在 messages forward 路径调用 | `checkCanonicalIngressUA` 调用点唯一：`gateway_service.go:4646`（`if c != nil && s.isCanonicalAnthropicOAuth(account)`）。count_tokens 路径（`:9944` 附近）不过此门。 |
| 出口 UA 被钉成 `claude-cli/2.1.170` 是 **edge→Anthropic** 跳，与入口门禁无关 | `applyCanonicalHTTPObserved`（`identity_service_tk_canonical_http.go:176`）、`identity_service.go:41`。即 UA 维度对 Anthropic 已抹平；真正封号信号是行为 cohort。 |
| body mimicry 在 haiku 上跳过 system 重写 | `gateway_service.go:4693` `if !strings.Contains(...,"haiku")`，导致非 CC haiku "有 CC 头无 CC system/billing block" |
| cc_only / fallback 在请求期的唯一闸 | `resolveGatewayGroup`（`gateway_service.go:2356`）：`!group.ClaudeCodeOnly || IsClaudeCodeClient(...)` 才放行，否则走 `FallbackGroupID` 或返回 `ErrClaudeCodeOnly` |

**推论**：空 UA 不是中继制造的噪声，是真实客户端信号穿透过来的。所以"拒空 UA"的风险不是"误杀 prod→edge 链路本身"，而是"误杀发空 UA 的真实匿名客户端"。这是一个**业务取舍**（要不要逼客户端带身份），不是一个被中继污染的技术误判——这恰好让 D1 的答案更干净：用 setting 控制，让运营选择。

---

## D1（核心）：入口 UA 门禁 deny-list → allow-list；空 UA 与中继如何处理

> ⚠️ **已被文末「方向修订」覆盖**：实际方向不是「在入口拒非 CC」，而是「放行非 CC、靠出口兜底转换」。本节的 allow-list 拒绝只是**两条策略之一**（reject-at-door），由 `anthropic_canonical_ingress_strict_enabled` 单独门控、默认关。

**结论**：保留 `checkCanonicalIngressUA` 现有 deny-list 作为**默认行为不变**；新增一个默认关闭的 setting `anthropic_canonical_ingress_strict_enabled`。当且仅当该 setting 开启时，门禁切换为 **allow-list 语义**：只放行 UA 前缀属于 `claude-cli/` 或 `claude-code/`（大小写不敏感、允许前导空白），**其余一律拒**——包括空 UA 与未知 UA。不开启时维持今天的 deny-list（空/未知放行），零回归。

**对空 UA**：strict 模式下**拒**。理由：§1 证实空 UA 是穿透的真实客户端信号，不是 prod→edge 伪影；运营关 cc_only 的本意若是"放非 CC 客户端进来"，那应由 cc_only flag 决定，而 canonical OAuth 账号（edge 上的个人订阅）正是最怕行为 cohort 的资产，必须要求显式 cc 身份。把"是否容忍匿名"交给 setting，而不是写死。

**对 prod→edge 可信中继跳**：**不引入"可信中继放宽"分支**。理由：§1 证实今天没有可靠的内部中继标识，凭空发明一个 `x-tk-relay` 共享密钥 header 会增加一条脆弱的、需要 prod 与所有 edge 同步配置的信道，违背"简洁"。正确做法是：**prod 侧才是该带 CC 身份的地方**——若运营要让非 CC 客户端经 prod 路由到 edge，应在 prod 入口保证转发的 UA 合规（这属于运营/配置范畴，超出本次代码准备）。代码侧只需保证：strict 门禁的判定基于真实穿透 UA，行为可预测、可一键关。

理由一句话：**不发明不存在的信任信号；把"是否容忍匿名/未知 UA"做成一个默认关、可秒回滚的运营开关，而非写死的策略。**

---

## D2：haiku mimicry 缺口

**结论**：strict 模式下，对**非 CC 的 haiku 请求也执行 system 重写 + 注入 billing block**（即移除 `:4693` 的 haiku 跳过分支的"短路"效果，但**用 setting 门控、追加实现，不删原分支**）。不采用"直接拒绝非 CC haiku"。

理由：缺口的本质是"有 CC 头无 CC system/billing block"的不一致 cohort 信号——**补全 mimicry 消除不一致**，比"拒绝"更端到端（haiku 是真实 CC 高频模型，拒绝会误伤）。实现上在 mimicry 分支里，当 strict setting 开启时，让 haiku 也走 `rewriteSystemForNonClaudeCode`。默认关闭时保持现状（haiku 仍跳过），零回归。

---

## D3：count_tokens 路径是否过 UA 门禁

**结论**：**过**。strict 模式开启时，在 count_tokens 路径（`gateway_service.go:9944` 附近、`isCanonicalAnthropicOAuth` 为真时）也调用 `checkCanonicalIngressUA`。

理由：count_tokens 与 messages 落在同一客户端、同一 canonical 账号上；若 messages 拒第三方而 count_tokens 放行，第三方仍能用 count_tokens 探测/消耗个人订阅，是 cohort 信号的旁路。门禁要在身份维度对**两条路径一致**才有意义。同样置于 setting 之后，默认关不变。

---

## D4：开关形态与默认值（宪法 §5.x 覆写默认 + admin setting）

> ⚠️ **已被文末「方向修订」覆盖**：本节的「单一开关统辖三处」是错的——目标配置需要「haiku 出口补全 ON、入口拒绝 OFF」，单开关无法表达。实际拆成**两个正交开关** `anthropic_canonical_ingress_strict_enabled`（#1#2 入口拒绝）与 `anthropic_canonical_haiku_mimicry_enabled`（#3 出口补全），均默认 false。详见文末修订节。

**结论**：新增单一布尔 setting，统辖 D1/D2/D3 三处收紧（一个开关，整套收紧行为，符合"聚焦/简洁"）。

- **Setting key**：`anthropic_canonical_ingress_strict_enabled`
- **常量**：`SettingKeyAnthropicCanonicalIngressStrictEnabled = "anthropic_canonical_ingress_strict_enabled"`，加到 `internal/service/domain_constants.go` 的 SettingKey 常量块（紧邻 `SettingKeyAnthropicRequestNormalizeEnabled`）。
- **默认值**：`false`（未显式开启 = 保持当前 upstream/deny-list 行为，零回归）。
- **读取函数签名**（新建 companion `internal/service/setting_service_tk_canonical_ingress.go`，仿 `setting_service_tk_kiro.go`）：
  ```go
  // IsAnthropicCanonicalIngressStrictEnabled reports whether the canonical
  // Anthropic OAuth ingress should enforce allow-list UA + haiku mimicry +
  // count_tokens UA gate. Defaults to false (keep deny-list / upstream behavior).
  func (s *SettingService) IsAnthropicCanonicalIngressStrictEnabled(ctx context.Context) bool
  ```
  实现：`GetValue(ctx, SettingKey...)`，`err != nil` 或 `!= "true"` 返回 `false`。
- **生效范围**：仅 anthropic OAuth + canonical TLS profile（继续被 `isCanonicalAnthropicOAuth` 守门）；apikey / 非 canonical OAuth 完全不受影响。
- **可见性/可切**：把字段接入 `GatewayForwardingSettings`/settings 视图与 admin setting handler（仿 `kiro_enabled` 的 tkApply 三件套：`updates[...]`/`defaults[...]`/解析 + view 字段 + handler changed 追加），让 admin 能在线切、可被 settings pubsub 热更（无需发版）。

理由：一个开关承载整套"收紧"语义，默认关=零回归，开=安全收紧；admin 可切 + pubsub 热更满足"不发版即可灰度/回滚"。

---

## D5：非 canonical 的 OAuth 账号（现网无，防漂移）

**结论**：**不拒绝、只告警/记录**（保持现状放行）。不在本次为它新增拦截。

理由：门禁整体被 `isCanonicalAnthropicOAuth` 守门——非 canonical OAuth 本就不进这套逻辑。现网无此账号；若未来漂移出现，应在**账号创建/调度可调度性**层面治理（绑定 canonical profile），而非在网关入口对一个不存在的形态加分支。可选：在 `isCanonicalAnthropicOAuth` 返回 false 但 `account.IsOAuth() && Platform==anthropic` 时打一条 `slog.Warn("anthropic oauth account without canonical TLS profile", account_id=...)`，作为漂移哨兵。**这条告警是 OUT-OF-SCOPE 的可选项，不阻塞本次。**

---

## D6：关 cc_only 丢失 fallback_group_id 分流能力

**结论**：**本次不做替代路由；明确留作运营用 cc_only flag + fallback_group 在组层面控制**。范围裁剪掉"非 CC→apikey 池"的代码侧重写。

理由：fallback 分流（`resolveGatewayGroup`，`gateway_service.go:2356`）是 cc_only 闸的副产物——cc_only=TRUE 时把非 CC 请求降级到 fallback 组。运营要保留"非 CC 走另一个池"的能力，**正确做法是保留该组的 cc_only=TRUE 并配 fallback_group_id**，而不是关掉 cc_only 再在代码里重造一套分流。本次目标是"让关 cc_only 安全可行"，不是"替运营决定关哪个组"。在代码侧重造非 CC→apikey 路由会与既有 fallback 机制重叠、增加调度复杂度，违背"聚焦"。**结论：OUT-OF-SCOPE，由运营按组配置 cc_only/fallback 决定。** 本次只保证：当运营选择关某组 cc_only 时，§D1–D4 的入口收紧能兜住裸奔的 canonical OAuth。

---

## D7：实施与灰度边界（秒级可回滚）

**本次代码准备不碰**：
- 线上 DB 的 `cc_only` flag（任何组）——纯运营动作。
- 任何 deploy / 发版 / VERSION bump / tag。
- push / 开 PR（除非另获明确授权）。
- prod→edge 中继的 UA 注入逻辑（不发明 `x-tk-relay`）。
- 非 CC→apikey 替代路由（D6 裁剪）。

**单 edge canary 与秒回滚**：所有新行为门控于 `anthropic_canonical_ingress_strict_enabled`（默认 false，admin 可切，settings pubsub 热更）。canary 流程（运营，非本次代码）：在**单台目标 edge**上开此 setting → 观察该 edge `served_200:no_available_429` 与 403 拒绝率 → 异常则在 admin 把 setting 关回 false，**无需 deploy、秒级生效**。这把"灰度"和"回滚"都做成数据面开关，而非代码/镜像切换。

---

## 工程师照做清单：确切文件与函数锚点

### 新增文件
1. `backend/internal/service/setting_service_tk_canonical_ingress.go`
   - `func (s *SettingService) IsAnthropicCanonicalIngressStrictEnabled(ctx context.Context) bool`（仿 `setting_service_tk_kiro.go`，默认 false）。

### 修改 `backend/internal/service/domain_constants.go`
2. SettingKey 常量块新增：
   `SettingKeyAnthropicCanonicalIngressStrictEnabled = "anthropic_canonical_ingress_strict_enabled"`（紧邻 `SettingKeyAnthropicRequestNormalizeEnabled` / `SettingKeyKiroEnabled` 风格注释）。

### 修改 `backend/internal/service/gateway_service_tk_canonical_oauth_guard.go`（追加，不重写既有函数）
3. 新增 `checkCanonicalIngressUAStrict(headers http.Header) error`：allow-list 语义——`ua` trim 后，前缀匹配 `claude-cli/` 或 `claude-code/`（`strings.HasPrefix(strings.ToLower(ua), ...)`）则放行；空 UA / 其它一律返回 `*CanonicalIngressUARejectedError`。
   - 保留原 `checkCanonicalIngressUA` 不动（默认路径）。

### 修改 `backend/internal/service/gateway_service.go`
4. messages forward 路径（`:4646` 附近，`if c != nil && s.isCanonicalAnthropicOAuth(account)` 块内）：
   `strict := s.settingService.IsAnthropicCanonicalIngressStrictEnabled(ctx)`；`strict` 时调 `checkCanonicalIngressUAStrict`，否则调原 `checkCanonicalIngressUA`。
5. haiku mimicry（`:4693` `if !strings.Contains(strings.ToLower(reqModel),"haiku")`）：把条件改为"非 haiku **或** strict 开启"——即 `if strict || !strings.Contains(...,"haiku")` 时执行 system 重写。**追加 `strict ||`，不删原 haiku 判断**，保证默认关时行为不变。
6. count_tokens 路径（`:9944` 附近，`shouldMimicClaudeCode` 之前的 `isCanonicalAnthropicOAuth` 守门处）：strict 开启时调用 `checkCanonicalIngressUAStrict(c.Request.Header)`，返回错误则按该路径既有错误约定返回（`s.countTokensError(...)` 或返回 error，与同文件 Antigravity 404 风格一致）。

### Setting 接线（仿 `kiro_enabled` 路径，让 admin 可切 + 热更）
7. `internal/service/setting_service.go`：`GatewaySettings`（或对应聚合结构）解析处追加该 key（默认 false）。
8. `internal/handler/dto/settings.go` + `internal/handler/admin/setting_handler.go`：追加 `AnthropicCanonicalIngressStrictEnabled bool` 字段 + `changed` 追加（仿 `kiro_enabled` 处 `:2223`）。
9. `internal/server/api_contract_test.go`：契约快照新增该 key=false（否则 contract test 红——见 memory「本地 go test 必须跑 ./...」）。

### 哨兵 / marker（宪法 §5.y.1）
10. 本次新增逻辑都在 `*_tk_*.go` companion，但**修改了 upstream-shaped `gateway_service.go`**（追加式）。提交需带 commit marker `upstream-touch-guarded`（锚点已 pin），或更新相应 `scripts/sentinels/*.json`。若 canonical guard 已有 sentinel 条目，在同一 commit 更新其 `must_contain`。

---

## 需要新增的单测清单

放 `backend/internal/service/gateway_service_tk_canonical_oauth_guard_test.go`（已存在）+ setting 测试新建 `setting_service_tk_canonical_ingress_test.go`：

1. **`checkCanonicalIngressUAStrict`**：
   - 空 UA → 拒（`*CanonicalIngressUARejectedError`）。
   - `claude-cli/2.1.170 (external, cli)` → 放行。
   - `claude-code/1.2.3` → 放行。
   - 第三方（`openai-python/1.0`、`python-requests/2.31`、未知 `foo/1.0`）→ 拒。
   - 大小写/前导空白变体（` Claude-CLI/2.2`）→ 放行。
2. **deny-list 不变性**：`checkCanonicalIngressUA`（原函数）空 UA / 未知 UA 仍放行（防回归）。
3. **setting 默认关 = 零回归**：`IsAnthropicCanonicalIngressStrictEnabled` 在无 setting / err / `"false"` 时返回 false；`"true"` 时 true。
4. **haiku mimicry 门控**（service 级或 body 重写单元）：strict=false 时 haiku 跳过 system 重写（现状）；strict=true 时 haiku 也被重写（注入 billing block）。
5. **count_tokens UA 门控**：strict=true 且 canonical OAuth + 第三方/空 UA → count_tokens 被拒；strict=false → 放行（零回归）。
6. **契约**：`api_contract_test.go` 快照含新 key=false。

---

## OUT-OF-SCOPE（明确不做）

- 改任何线上 `cc_only` DB flag、deploy、发版、push/PR（除非另获授权）。
- 发明 prod→edge "可信中继" header / 共享密钥信道（D1：不依赖不存在的信号）。
- 非 CC→apikey 池的替代路由 / 重造 fallback 分流（D6：交给运营用 cc_only+fallback_group 按组配置）。
- 非 canonical OAuth 账号的入口拦截（D5：现网无；最多打漂移告警，且为可选项）。
- 删除任何 upstream 符号（§5.x）。原 `checkCanonicalIngressUA`、haiku 跳过分支、deny-list 全部保留，新行为一律追加 + setting 门控。
- prod 侧入口 UA 合规化（属运营/配置）。

---

## 与宪法红线的对账

- **§5 upstream 隔离**：新逻辑全在 `*_tk_*.go`；`gateway_service.go` 仅追加 `strict` 取值 + 一两行分支 hook，不重写既有函数。✅
- **§5.x 禁删**：deny-list、haiku 分支、原函数全保留，用 setting 短路覆写默认。✅
- **§5.y.1 marker**：触 upstream-shaped 路径 → commit 带 `upstream-touch-guarded` 或更新 sentinel。✅
- **§6 接口完整性**：`IsAnthropicCanonicalIngressStrictEnabled` 是 `*SettingService` 具体方法，非接口新增方法，无需补 stub；若 `SettingService` 在网关侧经接口注入，需在该接口及所有 mock 补此方法（提交前 grep 确认）。⚠️ 实现者必查。
- **§8 层依赖**：service 调 service（settingService），不反向。✅
- **零回归**：默认 false 时 D1/D2/D3 全部退回当前行为。✅

---

## 审查补强（xj-review R-001..R-004，2026-06-11）

PR #691 审查发现四处缺口，已随 review fix commit 一并落地；D1–D7 结论不变，以下为对原文的修订与补充：

- **R-001（修订 D4 接线）**：`UpdateSettingsRequest.AnthropicCanonicalIngressStrictEnabled` 用 `*bool` preserve-on-absent（仿 `APIKeyACLTrustForwardedIP`），缺字段时回退 `previousSettings` 当前值。原因：本 PR 不带前端，旧 admin UI 整单保存的 payload 不含该字段，非指针 bool 会把 canary 期间已开启的 strict 静默重置回 false——防线被无声拆除。
- **R-002（修订 D4 读取实现）**：`IsAnthropicCanonicalIngressStrictEnabled` 不走每请求 `settingRepo.GetValue` 直查（canonical 热路径每请求 +1~2 次 DB 点查、DB 抖动 fail-open），改为并入共享 60s `gatewayForwardingCache`（singleflight + settings pubsub 刷新）——即 D4 原文"接入 GatewayForwardingSettings"的字面要求。
- **R-003（补 D7 观测）**：strict 403 在 edge 本地以 `MarkOpsClientBusinessLimited(LocalPolicyDenied)` 标记（messages handler 分支 + count_tokens 路径），否则 `permission_error` 落 phase=internal/P2 计入错误率，canary 一开拒绝量直接打污 error dashboards。
- **R-004（补 D1/D7 跨跳交互，prod 侧代码准备）**：edge strict 403 经 prod `cc-<edge>` 镜像 stub 回程时是**终端客户端身份问题，不是 stub/edge 健康问题**。不豁免则 1 分钟 3 次拒绝即推进 anthropic 3/3 阶梯、冷却 canary edge 的 stub（任意第三方客户端可把 canary edge 从 prod 池打掉，复刻 2026-05-31 no-available 放大器形态），且计入 `upstream_error_rate` 可触发 provider-health 假 P0。修复：`canonicalIngressRejectNeedle` 为 wire contract 单一源短语；prod 侧 `tkSkipRelayedCanonicalIngressRejectPenalty`（handle403 前置豁免，fail over 不进阶梯）+ ops 分类器 403 特例归 client-owned。边界纪律同 no-available skip：只认 TokenKey 自产短语，真 Anthropic 403（org disabled / bot challenge）原路计数。**灰度顺序推论：prod 必须先发版携带此豁免，才能在任何 edge 开 strict canary。**

---

## 方向修订（D1/D4，2026-06-11，运营授权）

原 D1/D4 把「canonical 入口收紧」设计成**单一开关统辖三处**（#1 入口 UA allow-list 拒绝 / #2 count_tokens 拒绝 / #3 非 CC haiku mimicry 补全），方向是「在入口拒掉非 CC」。运营复盘目标线上配置后授权调整方向，原结论作废，替换如下：

**目标配置（运营给定）**：default 组保持 cc-only=true；新建 default-fallback 组 cc-only=false；新增 2 个 edge anthropic OAuth 账号，**绑 canonical TLS**（对 anthropic OAuth 账号做兜底指纹保护是硬要求，绝不为放开而裸奔），**放行非 CC 客户端流量**，由 canonical TLS 那一套出口兜底转换（system 重写 + billing block + UA 钉死 + 指纹）把非 CC 流量洗成干净 CC cohort。

**关键发现**：在「账号必须 canonical」的硬约束下，#1#2（入口拒非 CC）与「放行非 CC + 出口兜底转换」**方向相反**——前者把后者想放进来的非 CC（第三方/空 UA）又拒在门口。而 #3（haiku 补全）是出口兜底转换的**必需补丁**（非 CC haiku 占 Claude Code 后台流量大头，缺它则半成品伪装裸奔出口，威胁订阅号 standing）。单开关把三者绑死 → 目标配置需要「#3=ON 且 #1#2=OFF」，单开关两种状态都给不了。

**修订**：拆成**两个正交开关**（均默认 false / 零回归，均 scoped canonical OAuth + 经 gatewayForwardingCache 热更）：

| 开关 | key | 管 | 策略 | 目标配置取值 |
|---|---|---|---|---|
| 入口 UA strict | `anthropic_canonical_ingress_strict_enabled` | #1 入口 allow-list + #2 count_tokens | 「reject at the door」：不容忍非 CC 上 canonical | **OFF** |
| haiku mimicry 补全 | `anthropic_canonical_haiku_mimicry_enabled` | #3 非 CC haiku 出口 system/billing 补全 | 「admit and launder」：放行非 CC，出口洗干净 | **ON** |

两个开关**不假设一起开关**：目标配置只开 haiku 补全、不开入口拒绝，即可达成「放行非 CC + 出口兜底、新订阅号不裸奔」。原 D1「拒空 UA」语义保留为另一条相反策略（reject-at-door）供需要时单独使用。

**R-003/R-004 适配**：strict 403 仅在「入口 UA strict」开关开启时产生，目标配置不开，故 R-003（business-limited 标记）/R-004（prod 跨跳豁免）在目标配置下不触发——逻辑保留不变，覆盖另一条策略被启用的场景。

**授权来源**：运营 2026-06-11 口头指示「按拆开关方向改 PR」「2 个新 edge 账号绑 canonical TLS 必须兜底保护」。frontmatter 的 approved_by 维持原值；本节为方向修订的可追溯锚点。
