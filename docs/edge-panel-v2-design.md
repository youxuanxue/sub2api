# Edge 二级展开 v2 — prod `/admin/accounts` 全页对应关系梳理与设计方案

> 状态：**REVIEW（未审批）** · 作者：edge-panel v2 · 基线：#885（v1.8.22 已发版）
> 本文是用户要求的「整体页面全部 review 一遍，把所有 stub 账号二级展开对应的 edge 账号对应关系都梳理清楚，给完善方案」的落地文档。
> **每一条技术事实都标注了代码出处（已逐一用工具核验，非推断）。**

---

## 0. 为什么要 v2（#885 留下的两个洞）

#885 已把 prod `/admin/accounts` 的 `cc-<edge>` 镜像 stub 行做成了可内联展开的「edge 面板」，并发版到 v1.8.22。但上线后用户反馈「页面没生效 / 没展开」，根因不是后端、不是部署，而是 **#885 的两个设计缺陷**：

1. **默认展开是「异常驱动」而非「默认全展开」。** 现状 `isStubPanelExpanded`（`frontend/src/utils/accountsEdgePanels.tk.ts:82-90`）的策略是：仅当 edge 不可达 / stub 限流 / stub 临时不可调度 / 任一 edge 账号异常时才自动展开，健康 edge 折叠。结果运维进页面看到的是**一排平铺的、看不出有二级内容的 stub 行**，以为功能没生效。这违背了核心理念「默认全展开、一目了然」（见 §1）。
2. **二级展开展示的是 edge 的「全量库存」，不是这把 stub key 真正调度的账号。** 现状 `useTkAccountsEdgePanels.ts:63` 用 `useTkEdgeAccounts('all')` 取 edge 全平台账号，展开 cc-us4 会看到 us4 上**所有**账号；但用户抓包确认：cc-us4 这把 stub api_key 在 edge us4 上只绑定了 `default` 组，只调度该组的 **2 个 anthropic 账号**。展开应当**精确对应这 2 个账号**，而不是 us4 的全部库存。

v2 解决这两点，并把「默认全展开 + 仅异常高亮 + 精确对应」作为贯穿所有 admin 页面的统一设计语言落地（理念见 memory `project_admin_default_expand_one_glance`）。

---

## 1. 核心产品理念（统一准绳，所有 admin 页面对齐）

1. **默认全部展开**：运维一进页面就看到所有细节（含二级 edge 账号），**无需切页、无需手动展开**。折叠是例外（嫌长了手动折），不是默认。
2. **当前页做所有操作**：查询 + 状态类管理（清限流 / 重置配额 / 重置临时不可调度 / 调度开关 / 主动查用量）都在 `/accounts` 内联完成；凭据类（编辑 / 重新授权 / 新建 / 删除）一键 handoff 到 edge 后台。prod 与 edge 统一治理，**让运维感觉不到 prod/edge 的接缝**。
3. **仅异常高亮**：正常数据平铺即可；只有异常（不可达 / 限流 / 冷却 / 账号异常）才特殊高亮（红点 / badge），让运维**一眼抓住需要关注的**。
4. **精确对应**：二级展开**只**显示这把 stub key 真正调度的 edge 账号，不混入无关库存。

---

## 2. 全页账号分类（systematic review — 本文核心）

把 prod `/admin/accounts` 上**每一种**账号类型走查一遍，判定它「是否有二级展开」以及「对应关系规则」。识别信号、是否有二级、规则全部来自代码核验。

| # | prod 账号类型 | 识别信号（代码出处） | 有二级展开？ | 二级对应关系规则 |
|---|---|---|---|---|
| 1 | **anthropic edge 镜像 stub** | `platform=anthropic` + `type=apikey` + `credentials.base_url=https://api-<edge>.tokenkey.dev`（`edgeIDPattern` `edge_accounts_aggregator_tk.go:56`；`isAnthropicMirrorStub` `:386`） | **是** | **组分区**：stub key → 它在 edge 上绑定的组（实测 `default`）→ 该组调度的 anthropic 账号。容量侧佐证：`SumConcurrencyAnthropicByGroup(ctx, "default")`（`edge_tk_capacity_handler.go:90`） |
| 2 | **kiro edge 镜像 stub** | `credentials.mirror_platform=kiro` + base_url 指向 edge（memory `surfacec_mirror_platform_per_pool`；容量 `SumConcurrencyByPlatform(ctx,"kiro")` `account_repo.go:199-203`） | **是** | **单池**：edge 对 kiro 是 single-pool-per-platform（`edge_tk_capacity_handler.go:70-71`、`account_service.go:85-89`），stub → 该 edge 上**全部** schedulable kiro 账号，无组分区 |
| 3 | **openai / antigravity / grok edge 镜像 stub** | `platform∈{openai,antigravity,grok}` + base_url 指向 edge（用户截图：openai-us3/us6、antigravity-us3/us4、grok-us4） | **是** | **由 stub key 绑定决定**：key 绑组 → 该组账号；key universal → 该平台整池。当前**未接入 surface-C 容量**（容量 handler 只认 anthropic/kiro，`:94`），但 edge 账号 API 经 `platform=all` 仍能枚举（`:42-50`） |
| 4 | **newapi channel 账号** | `platform=newapi` + `channel_type>0`（`account.go:57`「>0 means use New API adaptor bridge」） | **否** | 经 New API adaptor 桥接到**外部上游**（DeepSeek/Doubao/…），**背后没有 edge、没有二级账号**，它本身就是叶子。展开无意义 |
| 5 | **聚合器 as channel**（CPA 等） | 同 #4，`channel_type>0` 指向聚合器（memory `aggregator_as_channel_not_platform`） | **否** | 与 newapi channel 同构，是叶子；信号终结在聚合器内，无二级 |
| 6 | **gemini 账号** | `platform=gemini`，直连 Vertex/AI Studio（memory `gemini_media`），**无 `<plat>-<edge>` 镜像 stub** | **否** | 直连上游，无 edge 中继层，无二级 |
| 7 | **普通叶子账号**（prod 本地真实 OAuth/apikey） | base_url 为空或指向外部上游，**不**匹配 `api-<edge>.tokenkey.dev` | **否** | 终端账号，背后无物，无二级 |

### 结论（一句话）

> **只有「edge 镜像 stub」（任意平台，凭 base_url=`api-<edge>.tokenkey.dev` 识别）才有二级展开**；newapi/聚合器 channel、gemini、普通叶子账号都是叶子，平铺渲染即可，不给 chevron、不给二级面板。

### 关键纠偏（v2 相对 #885）

- #885 的 `isAnthropicMirrorStub` **硬编码了 `platform==anthropic`**（`edge_accounts_aggregator_tk.go:386`），导致 openai/antigravity/grok/kiro 的 stub 行**根本不被识别为可展开**——这正是用户截图里「openai/antigravity/grok 都没展开」的根因。v2 必须把识别条件从「anthropic + base_url」放宽到「**任意平台 + base_url 匹配 edgeIDPattern**」。
- ⚠️ **不要动 surface-C reconciler 自己的 `isMirrorStub`**（`anthropic_config_reconciler.go`，anthropic-only，容量回灌专用）。那是独立逻辑，与展示面板无关，改它会破坏容量回灌（memory 多次记录 surface-C 相关事故）。v2 只新增/放宽**聚合器侧**的识别，reconciler 侧零改动。

---

## 3. 对应关系的精确模型

### 3.1 拓扑二分（这是整个方案的地基）

edge 池的拓扑**不是统一的**，分两类（已核验）：

- **anthropic = 组分区（group-partitioned）**：一个 edge 上可能有多个组，stub key 绑定到其中一个组（实测 cc-us4 → `default`），只调度该组账号。容量回灌也只数该组：`SumConcurrencyAnthropicByGroup(ctx, anthropicDefaultGroupName)`（`edge_tk_capacity_handler.go:90`）。
- **非 anthropic（kiro 等）= 单池（single-pool-per-platform）**：edge 对该平台只有一个池，「每个 schedulable 账号就是运营池」，无组分区。容量回灌数全平台：`SumConcurrencyByPlatform(ctx, "kiro")`（`account_repo.go:194-211`，注释 195-198）。

### 3.2 统一规则（topology-agnostic，drive off stub key 绑定）

不需要在 TK 代码里 hardcode「哪个平台是组分区、哪个是单池」。**直接由 stub key 在 edge 上的绑定推导**，一条规则覆盖所有平台：

```
展开某 edge stub 显示的账号
  = edge.ListAccounts( platform = stub 的目标平台,
                       groupID  = 这把 stub key 在 edge 上绑定的 GroupID )
```

- **stub key 绑组（direct，`APIKey.GroupID` 非空，`RoutingMode="direct"`）** → groupID 过滤 → 精确返回该组账号。anthropic stub 命中此路（cc-us4 → default 组 → 2 个账号 ✅，与抓包一致）。
- **stub key 通配（universal，`RoutingMode="universal"`，无 GroupID）** → groupID=0 不按组过滤 → 返回该平台整池。kiro 单池命中此路 ✅。

`APIKey.GroupID *int64` + `RoutingMode string` + `RoutingModeDirect/Universal` + `IsUniversal()` 均已存在（`service/api_key.go:23-24,45,49,85`）。

### 3.3 为什么 edge 能「免费」知道这把 key 的组

prod aggregator 调 edge 时**就是用这把 stub 的 api_key 作 `x-api-key`**（复刻 `fetchEdgeAccounts` 扇出）。edge 的认证中间件 `GetByKey(callerKey)` 拿到的，正是**这把 key 在 edge 上的本地 APIKey 行**，其 `GroupID` 就是 cc-us4 在 us4 上绑定的 default 组。所以：

> **edge 端读「当前请求认证用的那把 key 的 GroupID」，零额外查询，就拿到了精确对应关系。** 每个 stub 调自己的 edge，天然各取各的组——无需 prod 传组、无需新表、无需新接口形状。

---

## 4. 后端实现（如何获取对应关系）

所有 hook 点已逐一核验，改动极小：

### 4.1 edge 端：让认证中间件暴露 caller key（+1 行）

`server/middleware/edge_capacity_auth_tk.go:63` 现在是 `c.Next()`，前面不 `c.Set`。加一行把已查到的 `apiKey` 塞进 context：

```go
c.Set(EdgeCallerAPIKeyCtxKey, apiKey) // v2: 供下游按 caller key 的组过滤
c.Next()
```

### 4.2 edge 端：ListAccounts 按 caller key 的组过滤

`handler/edge_tk_accounts_handler.go:251` 的第 8 参数（groupID）现在写死 `0`。改为读 caller key：

```go
groupID := int64(0)
if k, ok := c.Get(EdgeCallerAPIKeyCtxKey); ok {
    if ak, ok := k.(*service.APIKey); ok && ak.GroupID != nil && !ak.IsUniversal() {
        groupID = *ak.GroupID // direct key → 只看它绑定的组
    }
}
accounts, _, err := h.accounts.ListAccounts(ctx, 1, edgeAccountsMaxPageSize,
    edgeAccountsListFilter(platform), "", "", "", groupID, "", "priority", "asc")
```

- direct key（anthropic stub）→ 精确组账号；universal key（kiro stub）→ groupID=0 整平台池。**一条分支覆盖两种拓扑**。
- 复用现成 `account_groups` M2M 过滤（与网关调度同源），**零新查询**。
- ⚠️ 需新增 unit：direct key → 只返回组内账号；universal key → 返回整池；并保留旧行为（无 caller key 上下文时 = groupID 0）的回归。

### 4.3 聚合器：识别放宽到全平台 + per-edge → per-stub 重构（v2 核心后端改动）

> **读真代码后的修正（原 §4.3 低估了改动）**：现状聚合器是 **per-edge**——`ListByPlatform(ctx, anthropic)` 只加载 anthropic stub（`:257/:515`），`discoverEdgeTargets` 按 base_url **去重 keep-first**（`:326`，假设「一个 edge 一个 stub」），结果 **keyed by `edge_id`**。但精确对应是 **per-stub**：cc-us4 / openai-us4 / grok-us4 是**三把不同 key、同一个 `api-us4` host**，各展开各自的组——去重的 per-edge 抓取拿不到。故需把发现逻辑从 per-edge 改成 per-stub。

**(a) 谓词放宽**：`isAnthropicMirrorStub`（`:386`，`platform==anthropic`）→ 新增平台无关 `isEdgeMirrorStub(a, re)`：**只校验 `type=apikey` + base_url 匹配 `edgeIDPattern`**，不限平台。命名用 `isEdgeMirrorStub` 而非 `isMirrorStub`，避免与同包 reconciler 的 `isMirrorStub` **方法**视觉撞名（**R1**：聚合器内仍只一个谓词，旧 `isAnthropicMirrorStub` 调用点改完即删）。⚠️ **不碰** reconciler 的 `isMirrorStub` 方法（另一文件、容量回灌专用）。

**(b) per-stub 发现**：新增 `discoverStubTargets(stubs, re)`——**不去重**（每把 stub 是独立 target，携带其 prod 账号 id），输入是**全平台 apikey 账号**（见 (d)）。`edgeTarget` 加 `stubAccountID int64`；`EdgeAccountsResult` 加 `StubAccountID int64 json:"stub_account_id"`。

**(c) 复用扇出**：`fanout` 的并发 + `fetchEdgeAccounts`（用 `target.apiKey` 抓，已是 per-target）**原样复用**——每把 stub 用**自己的 key** 抓，edge 按 §4.2 caller-key 组过滤后**天然各取各的精确账号**。per-stub 抓取传 `platform = stub 的平台`（叠加组过滤双重 scope，universal 单池则平台过滤兜底）。

**(d) 全平台加载**：现状 `ListByPlatform(anthropic)` 只回 anthropic。per-stub 路径需全平台候选——本期对 5 个 stub 平台（anthropic/openai/antigravity/grok/kiro）各调一次 `ListByPlatform` 后合并（接口不变，遵 rule 6），再过 `isEdgeMirrorStub`。

**(e) 第二消费者（additive 决策）**：standalone `/admin/edge-accounts` 复用**同一** per-edge aggregate（keyed by edge_id）。本期它**承诺下线**（§7），故**不重构它**——per-stub 走**新增方法**（`AggregateByStub` / panel 专用），per-edge 老路径原样留给 standalone，待其下线时一并消亡。两条发现函数共享同一扇出，非两套扇出。

**(f) 行标记**：`MirrorStubEdgeID`（`:425`）改用 `isEdgeMirrorStub`，使 admin list（`account_handler.go:351`，已遍历全平台账号）给所有平台 stub 行打 `edge_id` → 前端识别可展开，无需改加载。

### 4.4 prod accounts 列表 DTO：携带 `edge_id`（#885 已有）

`AccountWithConcurrency.EdgeID`（`handler/admin/account_handler.go:196,351`，#885 落地）已存在；放宽谓词后自动覆盖全平台 stub，无需新字段。前端 `panelForStub` 改为按 **stub 账号 id**（非 edge_id）取精确切片。

### 4.4 prod accounts 列表 DTO：携带 `edge_id`（#885 已有）

`AccountWithConcurrency.EdgeID`（`handler/admin/edge_accounts_handler_tk.go` 等，#885 落地）已存在；放宽谓词后自动覆盖全平台 stub，无需新字段。

### 4.5 grok 平台补进 edge 账号 allowlist（顺手修，可选）

`edge_tk_accounts_handler.go:42-50` 的 `edgeAccountsSupportedPlatforms` 缺 `grok`。v2 用 `platform=all` 不受影响，但若未来要按 grok 单独窄查会 400。**建议本 PR 顺手补 `service.PlatformGrok: {}`**（一行，纯插入），消除这个静默不一致。

---

## 5. 前端实现

### 5.1 默认全展开（改 expand 状态机）

`utils/accountsEdgePanels.tk.ts` 的 `isStubPanelExpanded`：把「默认 = 异常驱动」改为「**默认 = 展开**」：

```
override 显式设置  → override          // 用户手动折叠/展开优先，持久化
searching         → 展开               // 搜索命中自动展开（保留）
否则              → true               // ★ 默认全展开（v2 核心翻转）
```

异常不再决定「是否展开」（默认就全展开了），而是决定「**高亮**」（见 5.3）。

### 5.2 每行 chevron + 折叠态一行摘要

#885 折叠态无入口（不可见）。v2 每个 stub 行加 per-row chevron；折叠时显示一行摘要「N 账号 · M 可调度 · ⚠K 异常」（计数复用 `edgePanelCounts`，`accountsEdgePanels.tk.ts:67`），让折叠态也「看得见、点得开」。

### 5.3 仅异常高亮（first-class）

健康账号平铺；异常账号（`edgeAccountIsAbnormal`，`:33`）+ 异常 edge（`edgePanelHasAnomaly`，`:48`）用红点/badge 高亮，面板头部把异常计数前置。把 #885 里「异常 → 展开」的语义平移成「异常 → 高亮」。

### 5.4 精确对应（前端跟随后端）

`useTkAccountsEdgePanels.ts:63` 的 `useTkEdgeAccounts('all')` 仍取全 edge 数据用于 fleet 概览；但**每个 stub 面板**展示的账号由后端按 caller key 的组过滤后返回（§4.2），前端按 `edge_id` 取该 stub 对应的精确切片即可，无需前端再过滤。

### 5.5 凭据边界不变（#885 已对）

状态类 op 走 prod thin-proxy 内联；凭据类走 admin-session handoff「在 edge 管理 ↗」。密钥永不流经 prod。v2 不改这条边界。

### 5.6 面板三态必须都设计（R2 — 默认全展开的直接后果）

#885 只在「异常」时展开，所以从没设计过「展开后正常/不可达/空」的完整态。v2 默认全展开把**每个 stub 的展开态都变成常驻可见**，三态缺一就会暴露半成品：

1. **健康** → 精确账号子表（§5.4），头部脚注（§6.1）。
2. **edge 不可达 / 拉取失败** → 面板内**内联错误条 + 重试按钮**（复用 `edgeError`/`refreshEdges`，`useTkAccountsEdgePanels.ts:189-191`），**不是**一个坏掉的空展开；stub 行本身红点高亮。
3. **绑组但组内 0 账号**（含 universal key 命中空平台池）→ **可操作空态**「该组在 `<edge>` 上暂无账号，点此到 edge 配置 ↗」，不是空白区。

三态都走同一面板骨架，仅主体区按 `edgeError / loading / accounts.length` 分支——结构上一处分发，不散落。

### 5.7 精确过滤的「读得懂」+ 逃生口（R3）

精确组过滤会让面板里的账号数**少于** edge 全量（us4 全量 → default 组 2 个）。为防运维误以为「账号丢了」：

- 头部脚注明示来源与计数：**「调度自 `<edge>` 的 `<group>` 组 · 共 N 个」**（universal 单池则为「`<edge>` 的 `<platform>` 全池 · 共 N 个」）。
- 始终保留「**管理该 edge 全部账号 ↗**」handoff 作为看全量库存的逃生口。
- 这是**刻意的旅程**：默认看「这把 key 真正调度什么」，一键可达「这个 edge 上的全部」。

### 5.8 用「行密度 + 异常置顶」消解长度，而不是用上限管理长度（乔布斯复审追加）

> 「页面太长」不是展开的问题，是**行太胖、问题没浮上来**的问题。别用「藏起来」解决长度。

默认全展开 + N 个 edge 确实会长。错误解法是设「超 N 自动折叠」——那又把内容藏回去、违背核心理念。正确解法两件事，把长度**从根上消解**：

1. **行做到极致紧凑**：每个 edge 账号一行（不是一张胖卡片），平台徽章/能力/状态/容量/用量在一行内对齐密排。长度来自胖行，不来自展开——压扁行，30 个账号跨 6 个 edge 仍能近一屏扫完。
2. **异常永远置顶排序**：异常账号 / 异常 edge 始终排在各自分组最上面，不管页面多长，需要管的永远在首屏上方。长度因此不再是问题。

这条是「仅异常高亮」（§1.3）的排序版：高亮让坏的**跳出来**，置顶让坏的**够得到**。两者合一 = 运维扫一眼，该管的全在眼前、坏的自己浮上来。

---

## 6. 乔布斯决策点（已拍板的取舍）

1. **组归属用「脚注」不用「chip」**：面板头部一行小字「调度自 `<edge>` 的 `<group>` 组 · 共 N 个」（universal 单池为「`<edge>` 的 `<platform>` 全池 · 共 N 个」），不做花哨标签——信息够用、不抢视觉。计数让精确过滤「读得懂」（见 §5.7）。
2. **异常高亮 first-class，默认全展开**：默认全展开后，异常的价值从「决定展开」变为「决定注意力」；红点/badge 是页面上唯一需要「跳出来」的东西。
3. **handoff 文案统一为「管理该 edge 全部账号 ↗」**：一键直达，不在文案里堆细节。
4. **空组 / universal 的可操作空态**：stub key 绑了组但组内 0 账号 → 显式空态「该组在 edge 上暂无账号，点此到 edge 配置 ↗」，不是空白。
5. **叶子账号零装饰**：newapi/聚合器/gemini/普通账号不给 chevron、不给摘要行，和 #885 之前完全一致——避免给「展开了什么都没有」的假入口。

---

## 7. 明确不做 / 不碰

- **不碰** surface-C `anthropic_config_reconciler.go` 的 `isMirrorStub` 与容量回灌（独立逻辑）。
- **不给** newapi/聚合器 channel、gemini、普通叶子账号二级面板（§2 结论）。
- **本期保留** `/admin/edge-accounts`（只读 fleet 鸟瞰），**但承诺下线**：两个门展示同一批东西是困惑源（乔布斯复审），路线图明确——其唯一剩余价值（跨 edge 概览）下一期上移到 /accounts 顶部后即下线 `/edge-accounts`。本 PR 不做迁移，但这不是「也许哪天」，是已定的下一步。
- **不引入** Ent schema 改动（`edge_id` 派生、不入库）。
- **不让** 凭据流经 prod（边界不变）。

---

## 8. 验证计划

- **Go unit**：
  - edge ListAccounts 组过滤：direct key → 仅组内账号；universal key → 整平台池；无 caller key → 旧行为（groupID 0）回归。
  - 认证中间件：成功路径 `c.Get(EdgeCallerAPIKeyCtxKey)` 拿到非空 key；失败路径不 set。
  - `isMirrorStub`（放宽版）：openai/antigravity/grok/kiro + base_url → true；非 stub → false；**reconciler 侧 anthropic-only 谓词回归不变**。
  - `MirrorStubEdgeID` 全平台：`api-us4...` → `us4`。
- **前端 vitest**：`isStubPanelExpanded` 默认 true 真值表；折叠摘要计数；异常高亮谓词。
- **E2E（verify skill，真实 UI / Playwright）**：5 类平台 stub 全部默认展开；展开 cc-us4 **只见 default 组的 2 个账号**（精确对应）；折叠 chevron 可见可点；异常账号高亮；newapi/leaf 行无 chevron；handoff 自动登录。
- 提交前 `scripts/preflight.sh` 全绿；任何前端改动 `pnpm build` 重建并提交 `frontend-source.json`。

## 9. 风险与回退

1. **组过滤改错池**：universal 误判成 direct 会把整池缩成空组。→ 严格用 `IsUniversal()` 判定 + 上述三态 unit 覆盖。
2. **谓词放宽误伤**：`isMirrorStub` 放宽后可能把某个恰好 base_url 像 edge 的非镜像账号识别为 stub。→ `edgeIDPattern` 锚定 `^https?://api-<id>.tokenkey.dev/?$` 精确正则，已够紧。
3. **默认全展开 + 多 edge → 长页面**：→ 折叠态一行摘要 + 顶部「全部折叠」总开关 + 每行持久化，运维可一键收。
4. **grok allowlist 补漏**：纯插入，零回归风险。
5. **§5 upstream isolation**：识别放宽、组过滤全落在 `*_tk_*.go`；`DataTable.vue`/`AccountsView.vue` 保持 #885 的纯插入形态；不新增 upstream 文件重写。
6. **R4 — stub key RoutingMode 误配 foot-gun**：若某 anthropic stub key 在 edge 上被误配成 universal（本应 direct 绑组），组过滤失效 → 面板显示该 edge **全部** anthropic 账号（**过宽但同平台，安全降级**，不会串到别的平台或别的 edge）。依赖 edge 侧绑定正确——cc-us4 已抓包确认为 direct→default 组。缓解：面板脚注会暴露「全池 · 共 N 个」与预期「default 组 · 2 个」的差异，运维一眼可辨误配；不额外加 prod 侧校验（edge 绑定是 edge 的真相源）。

---

## 附：本文涉及的已核验代码锚点

| 事实 | 文件:行 |
|---|---|
| 7 平台常量 | `domain/constants.go:23-39` |
| edgeIDPattern | `service/edge_accounts_aggregator_tk.go:56` |
| isAnthropicMirrorStub（anthropic-only，待放宽） | `service/edge_accounts_aggregator_tk.go:386` |
| anthropic 组分区容量 | `handler/edge_tk_capacity_handler.go:90` |
| kiro 单池容量 | `handler/edge_tk_capacity_handler.go:92`、`repository/account_repo.go:199-203` |
| 容量 handler 只认 anthropic/kiro | `handler/edge_tk_capacity_handler.go:94` |
| edge ListAccounts groupID=0（待改） | `handler/edge_tk_accounts_handler.go:251` |
| edge 账号 allowlist（缺 grok） | `handler/edge_tk_accounts_handler.go:42-50` |
| 认证中间件不 c.Set（待加） | `server/middleware/edge_capacity_auth_tk.go:63` |
| APIKey.GroupID/RoutingMode/IsUniversal | `service/api_key.go:23-24,45,49,85` |
| newapi channel_type 桥接外部 | `service/account.go:57` |
| #885 默认异常驱动展开（待翻转） | `utils/accountsEdgePanels.tk.ts:82-90` |
| #885 面板取 'all' 全量（待精确化） | `composables/useTkAccountsEdgePanels.ts:63` |
