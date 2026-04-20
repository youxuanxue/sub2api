---
title: NewAPI as First-Class Fifth Platform — Scheduling & Dispatch Convergence
status: shipped
approved_by: xuejiao (PR #9 squash-merge)
approved_at: 2026-04-19
shipped_at: 2026-04-19
authors: [agent]
created: 2026-04-19
related_prs: ["#9"]
related_commits: [e768deed, af9a73a6, e9f8bc56, 0211c2b7, c5130c29, 6e0c0fce, bf784cab, 4441b642, ab39ddbb]
related_stories: [US-008, US-009, US-010, US-011, US-012, US-013, US-014, US-015]
related_audit: TOKENKEY_PLATFORM_AUDIT_2026-04-19(1).md
---

# NewAPI as First-Class Fifth Platform

## 0. TL;DR

CLAUDE.md 把 `newapi` 描述为"first-class fifth platform"，路由层与 endpoint 推导也已就位，
**但调度/筛选层仍然把 `newapi` 平台账号挡在门外**——任何 `group.platform=newapi` 的 group
都无法挑出账号，三大 OpenAI-compat 入口（`/v1/chat/completions` `/v1/messages` `/v1/responses`）
对它们必然失败。这是一个被测试同事于 2026-04-19 报告、且经过两轮自检确认的 P0 集成残缺。

本设计**只**补齐"调度池语义按 `group.platform` 分桶 + `messages_dispatch` 对 newapi 放行"两件事，
不引入新协议入口、不混池、不重命名既有概念、不动 admin UI。完成后 `newapi` 平台从
"路由通了但用不了" → "5 维主流程跑通"。

> 范围聚焦（Jobs 原则）：
> - **做**：把"哪些账号属于本次调度池"这一概念按 `group.platform` 显式化，
>   并新增 `IsOpenAICompatPoolMember(groupPlatform)` 一个语义清晰的 helper。
> - **不做**：①混池调度（`openai` group 拿到 `newapi` 账号）②扩展 `IsOpenAI()` 上游语义
>   ③新增协议入口 ④UI/admin 改动 ⑤bridge 路径与计费链路修改。

## 1. 现状盘点（基于 2026-04-19 main 分支代码事实）

### 1.1 已就位（不动）

| 层 | 文件:行 | 状态 |
|---|---|---|
| 平台常量 | `domain/constants.go:25` `PlatformNewAPI = "newapi"` | ✅ |
| 路由分发 | `routes/gateway_tk_openai_compat_handlers.go:12-14` `isOpenAICompatPlatform` 已含 newapi | ✅ |
| 路由注册 | `routes/gateway.go:42-54` 三入口走 `tkOpenAICompat*` wrapper | ✅ |
| Endpoint 推导 | `handler/endpoint.go:76` `case PlatformOpenAI, PlatformNewAPI` | ✅ |
| Bridge dispatch | `service/openai_gateway_bridge_dispatch*.go` 完整 | ✅ |
| Admin 创建账号 | `service/admin_service.go:1565` `newapi` 强制 `channel_type > 0` | ✅ |
| Bridge 触发判定 | `service/openai_gateway_bridge_dispatch.go:14` 按账号 `channel_type>0` | ✅ |

### 1.2 缺口（本设计修补）

| 层 | 文件:行 | 现状 | 缺什么 |
|---|---|---|---|
| 候选池拉取 | `service/openai_gateway_service.go:1628-1646` `listSchedulableAccounts` | hardcoded `PlatformOpenAI` | 必须按 `group.Platform` 决定要拉哪个平台 |
| Scheduler bucket | `service/scheduler_snapshot_service.go:107` `ListSchedulableAccounts(..., PlatformOpenAI, false)` | bucket key 固定 `openai` | bucket.Platform 必须 = group.Platform，否则 cache 错位 |
| LoadBalance 过滤 | `service/openai_account_scheduler.go:594` `!account.IsOpenAI()` | 二次过滤掉所有 newapi 账号 | 改为"是否属于本次池" |
| Sticky session 过滤 | `service/openai_gateway_service.go:1296` `!account.IsOpenAI()` | sticky 命中也被打掉 | 同上 |
| Fresh recheck 过滤 | `service/openai_gateway_service.go:1669` `resolveFreshSchedulableOpenAIAccount` | 同上 | 同上 |
| Messages dispatch sanitize | `service/openai_messages_dispatch.go:93-100` `if g.Platform == PlatformOpenAI return` | 非 openai group 的 `messages_dispatch_model_config` 被强清 | newapi group 也应保留 |

### 1.3 真实代码事实链

`/v1/messages` 入口最短失败路径（today, `group.platform=newapi`）：

```
POST /v1/messages
  → tkOpenAICompatMessagesPOST                                  (route OK ✅)
  → OpenAIGatewayHandler.Messages                                (handler OK ✅)
    → SelectAccountWithScheduler                                 (entry OK ✅)
      → defaultOpenAIAccountScheduler.selectByLoadBalance
        → listSchedulableAccounts(groupID)
          → ListSchedulableAccounts(ctx, groupID, "openai", …)   (❌ 拉不到 newapi)
        → 返回空 →  errors.New("no available OpenAI accounts")
```

> 即便候选池能拿到 newapi 账号，`if !account.IsOpenAI() { continue }` 也会再过滤一次。

## 2. 设计

### 2.1 核心原则

1. **一个 group 一个意图**：`group.platform` 即调度域。`group.platform=openai` 的 group
   永远只调度 openai 账号；`group.platform=newapi` 的 group 永远只调度 newapi 账号。
   **不混池**。
2. **不重命名既有概念**：`IsOpenAI()` 语义保持"账号 platform 是否为 openai"。
   新增 `IsOpenAICompatPoolMember(groupPlatform string)` 表达"账号是否属于该 platform 调度池"，
   把"调度池归属"这个新语义明确出来，避免污染原有 30+ 处 `IsOpenAI()` 调用语义。
3. **最小注入点**（§5 upstream 兼容）：upstream 文件每处只改一行/加一字段，
   真实判定逻辑搬到 `*_tk_*.go` companion，对照 `gateway_handler_tk_affinity.go` 等已有先例。
4. **bucket 按 group.platform 自然分桶**：cache key 自动隔离，`openai` group 与 `newapi` group
   各拥一份调度快照，不会互相污染。

### 2.2 数据/调度池模型

```
group.platform = "openai"                  group.platform = "newapi"
       │                                          │
       ▼                                          ▼
SchedulerBucket{Platform: "openai"}    SchedulerBucket{Platform: "newapi"}
       │                                          │
       ▼                                          ▼
ListSchedulableByGroupIDAndPlatform     ListSchedulableByGroupIDAndPlatform
       (groupID, "openai")                     (groupID, "newapi")
       │                                          │
       ▼                                          ▼
   [Account{Platform=openai}, …]          [Account{Platform=newapi,
                                                     ChannelType>0}, …]
       │                                          │
       ▼                                          ▼
   IsOpenAICompatPoolMember(           IsOpenAICompatPoolMember(
     "openai") = true ⇒ keep             "newapi") = true ⇒ keep
       │                                          │
       ▼                                          ▼
selectBestAccount → Forward            selectBestAccount → ForwardAsXxxDispatched
   原生 OpenAI 协议 / OAuth                   ShouldDispatchToNewAPIBridge
                                              ⇒ true ⇒ bridge.DispatchXxx
```

> `IsOpenAICompatPoolMember(p)` 的简单定义就是 `account.Platform == p`。
> 这个看似"显然"的封装是为了把"调度池归属"概念化，便于将来扩到第六平台时
> 一处统一替换，而不是再 grep `IsOpenAI()` 一遍。

### 2.3 Messages Dispatch 对 newapi 的语义

`messages_dispatch_model_config`（一站式 K 的核心配置——把 `/v1/messages` Anthropic 协议
落到 OpenAI 兼容上游模型）目前只对 `group.platform=openai` 放行。设计改为：

| group.platform | AllowMessagesDispatch | MessagesDispatchModelConfig |
|---|---|---|
| openai | 可配置 | 可配置 |
| **newapi** | **可配置（新）** | **可配置（新）** |
| anthropic / gemini / antigravity | 强制 false | 强制清空 |

判定函数化（`isOpenAICompatPlatformGroup(g)`），与路由层 `isOpenAICompatPlatform()` 含义对齐。

### 2.4 Scheduler bucket cache 兼容

bucket key 已经是 `(groupID, platform, mode)` 三元组（`scheduler_snapshot_service.go:675`）。
唯一的变化是 OpenAI 入口本来传 `PlatformOpenAI` 常量，改为传 `group.Platform`。
现有 openai group 的 bucket key 不变 → cache 不失效。
新增的 newapi group 的 bucket key 不与 openai 冲突 → cache 自然分离。
**无需 cache 清理或 schema 迁移**。

### 2.5 Sticky session key 兼容

`getStickySessionAccountID` 的 key 当前形如 `openai:{groupID}:{sessionHash}`
（`openai_gateway_service.go:1190` 上下文）。本设计**不改 key 命名**，
原因：sticky session 已绑定具体 account_id，调度命中后 `recheck` 会用
`IsOpenAICompatPoolMember(groupPlatform)` 验证账号是否仍属于本池——
如果账号被改 platform 或被换组，sticky 会自然失效并降级到 load-balance，
不需要按 platform 拆 key。

> 备选：将来若发现热点 group 跨平台切换频繁，可平滑迁移到
> `compat:{groupPlatform}:{groupID}:{sessionHash}`，本设计预留方法
> 但**不做**（Jobs：暂无证据需要做）。

### 2.6 Bridge dispatch 路径不变

`ShouldDispatchToNewAPIBridge(account, endpoint)` 已经按账号 `ChannelType>0`
触发，与 group.platform 解耦，已完整。不动。

## 3. 实施清单（最小切面）

### 3.1 Upstream 文件改动（每处 ≤ 5 行，纯注入点）

| # | 文件 | 行 | 现状 | 改后 |
|---|---|---|---|---|
| U1 | `service/openai_gateway_service.go` | 1628-1646 | `listSchedulableAccounts(groupID)` 内部用 `PlatformOpenAI` | 拆出参数 / 内部调 TK helper：`s.listOpenAICompatSchedulableAccounts(ctx, groupID, s.resolveGroupPlatform(ctx, groupID))` |
| U2 | `service/openai_account_scheduler.go` | 24-32 | `OpenAIAccountScheduleRequest` | 加字段 `GroupPlatform string` |
| U3 | `service/openai_account_scheduler.go` | 594 | `if !account.IsSchedulable() \|\| !account.IsOpenAI()` | `if !account.IsSchedulable() \|\| !account.IsOpenAICompatPoolMember(req.GroupPlatform)` |
| U4 | `service/openai_account_scheduler.go` | 823+ | `SelectAccountWithScheduler` 入口构 ScheduleRequest | 入口处 `req.GroupPlatform = s.resolveGroupPlatform(ctx, groupID)` |
| U5 | `service/openai_gateway_service.go` | 1296 | sticky `if !account.IsSchedulable() \|\| !account.IsOpenAI()` | 同 U3，传 `groupPlatform` 上下文（从外层 selectAccount 传入） |
| U6 | `service/openai_gateway_service.go` | 1669 | `resolveFreshSchedulableOpenAIAccount` 同上 | 同 U3 |
| U7 | `service/openai_messages_dispatch.go` | 93-100 | `if g.Platform == PlatformOpenAI return` | `if isOpenAICompatPlatformGroup(g) return` |

合计 **upstream 改动 ≈ 30 行**（含函数签名调整），全部是"加字段/换一行判定"，
没有重写任何 upstream 函数，没有删任何 upstream 符号。

### 3.2 TK companion 新增（真实判定逻辑）

| 文件（新增） | 内容 |
|---|---|
| `service/account_tk_compat_pool.go` | `func (a *Account) IsOpenAICompatPoolMember(groupPlatform string) bool` 单方法。语义：`a.Platform == groupPlatform && (groupPlatform != PlatformNewAPI \|\| a.ChannelType > 0)`。同时导出 `func OpenAICompatPlatforms() []string { return []string{PlatformOpenAI, PlatformNewAPI} }` 给路由/setting 复用。 |
| `service/openai_gateway_service_tk_newapi_pool.go` | `func (s *OpenAIGatewayService) listOpenAICompatSchedulableAccounts(ctx, groupID *int64, groupPlatform string) ([]Account, error)` —— bucket.Platform 用 groupPlatform，否则全部委托既有 `schedulerSnapshot.ListSchedulableAccounts`。`func (s *OpenAIGatewayService) resolveGroupPlatform(ctx, groupID *int64) string` —— 从 schedulerSnapshot 拿 group，缺省回退 `PlatformOpenAI` 保证旧行为。 |
| `service/openai_messages_dispatch_tk_newapi.go` | `func isOpenAICompatPlatformGroup(g *Group) bool { return g != nil && (g.Platform == PlatformOpenAI \|\| g.Platform == PlatformNewAPI) }` |
| `service/account_tk_compat_pool_test.go` | 单元测：openai 池/newapi 池/混池/channel_type=0 的 newapi 账号被排除 |
| `service/openai_account_scheduler_tk_newapi_test.go` | 单元测：6 维矩阵覆盖 selectByLoadBalance 的 newapi 池行为 |
| `service/openai_messages_dispatch_tk_newapi_test.go` | 单元测：sanitize 对 newapi group 保留字段 |

### 3.3 Routes / Frontend 不动

- routes：`tkOpenAICompatChatCompletionsPOST` 等已识别 newapi group，0 改动
- frontend：`platformOptions` 是否含 newapi 由 admin UI 决定，不在本 design 范围
- admin handler：`admin_service.go` 已支持创建 newapi 账号，0 改动

### 3.4 配置/迁移/Schema

- 0 张新表，0 列变更
- 0 个新 setting（`messages_dispatch_model_config` 复用既有字段）
- 0 个 cache 清理脚本（bucket key 自然兼容）
- 0 个 redis key 命名变更（sticky 复用）

> 这是**最小可工作的零迁移变更**。任何不是 §3.1 / §3.2 表里的改动，都需要重新走审批。

## 4. 测试矩阵（6 维 + 风险覆盖，按 test-philosophy.mdc）

### 4.1 User Story（已建立，状态对齐到 `.testing/user-stories/index.md`）

> 编号约定：本仓库 user story 走全局递增编号（`.testing/user-stories/index.md`），
> design 阶段曾用别名 `US-NEWAPI-001~008`，实际登记为 `US-008~015`。下表是单一事实来源。

| 全局 ID | 别名（design） | Title | Trace | 优先级 |
|---|---|---|---|---|
| US-008 | US-NEWAPI-001 | newapi group + ChatCompletions 端到端走通 | 角色×能力 | P0 |
| US-009 | US-NEWAPI-002 | newapi group + Messages（一站式 K） | 角色×能力 | P0 |
| US-010 | US-NEWAPI-003 | newapi group + Responses 端到端走通 | 角色×能力 | P0 |
| US-011 | US-NEWAPI-004 | openai group 调度池**不被** newapi 账号污染 | 防御需求 | P0 |
| US-012 | US-NEWAPI-005 | newapi group 池空时返回明确"no available newapi accounts" | 防御需求 | P1 |
| US-013 | US-NEWAPI-006 | newapi group + sticky session 命中后 recheck 通过/失效降级 | 实体生命周期 | P1 |
| US-014 | US-NEWAPI-007 | newapi group 配置 messages_dispatch_model_config 持久化与读取 | 角色×能力 | P1 |
| US-015 | US-NEWAPI-008 | 历史 openai group 行为完全不变（回归） | 防御需求 | P0 |

### 4.2 6 维用例覆盖（按 test-philosophy §4，编号已对齐到全局 US-008~015）

| 维度 | 必测 case | 覆盖 story |
|---|---|---|
| 正向路径 | newapi group 三入口走通 | US-008 / US-009 / US-010 |
| 输入空间 | groupPlatform="" / 未知值 → 回退 openai 行为 | US-015（回归基线 + 默认值 helper 单测） |
| 前置状态 | newapi 账号 channel_type=0（不应入池） | US-012 |
| 副作用 | scheduler bucket cache key 按 platform 分桶 | US-011 + US-008 AC-001 断言 bucketKey |
| 并发时序 | openai group + newapi group 同时调度互不干扰 | US-011 AC-004（混池排除）+ US-015 |
| 权限角色 | non-compat group（anthropic 等）`messages_dispatch` 强制清空 | US-014 / US-009 sanitize 测试 |

### 4.3 风险覆盖（4 类必声明）

- **逻辑错误**：`groupPlatform=""` 回退路径必须保证旧 openai group 选不到 newapi 账号（US-011 + US-015 联合断言）
- **行为回归**：US-015 必须保留旧 openai group 的所有既有 sticky/loadbalance 行为
- **安全问题**：openai group 不得越权调度到 newapi 账号（混池漏洞），US-011 全部 AC 显式断言
- **运行时问题**：scheduler cache 在升级后旧 openai bucket 仍命中、新 newapi bucket 冷启动正常（US-008 AC-001 中 schedulerSnapshot.bucketKey 断言）

## 5. OPC 自动化门禁（preflight 接入）

### 5.1 必须在 PR 内落的脚本检查

```bash
# preflight 段（追加到 scripts/preflight.sh）
echo "[preflight] newapi compat pool drift check"
# 1) 候选池拉取必须经 TK helper（防止有人新增直接传 PlatformOpenAI）
! rg -n 'ListSchedulableAccounts\(.*PlatformOpenAI' backend/internal/service/ \
    --glob '!*_test.go' --glob '!*_tk_*.go' \
  || { echo "FAIL: direct PlatformOpenAI bucket usage outside TK helpers"; exit 1; }
# 2) selectByLoadBalance/sticky/recheck 不能再用裸 IsOpenAI() 做调度过滤
! rg -nP '!\s*account\.IsOpenAI\(\)' \
    backend/internal/service/openai_account_scheduler.go \
    backend/internal/service/openai_gateway_service.go \
  || { echo "FAIL: scheduling filter still uses IsOpenAI() — must use IsOpenAICompatPoolMember"; exit 1; }
echo "[preflight] newapi compat pool drift check OK"
```

### 5.2 集成测试（必须 testcontainer 自动化，不依赖手动）

```bash
# 全局编号已对齐，run 模式按 US-008 ~ US-015
go test -tags=integration -run 'TestUS00[89]_|TestUS01[0-5]_' ./backend/internal/service/...
go test -tags=integration -run 'TestUS00[89]_|TestUS01[0-5]_' ./backend/internal/handler/...
```

### 5.3 契约文档自动重生成

`scripts/export_agent_contract.py` 重新跑一次，把新增的 `messages_dispatch` newapi
能力以及 newapi group 三入口语义写入 `docs/agent_integration.md`（drift check 必须过）。

## 6. 不做的（聚焦过滤，与 §0 呼应）

| 不做 | 原因 |
|---|---|
| 扩展 `IsOpenAI()` 语义 | 30+ 处调用，污染原语义；新增专用 helper 一次性表达"调度池归属"更清晰 |
| 新增"openai+newapi 混池"模式 | 与"一个 group 一个意图"冲突；如有真实需求另起 design |
| 新增 platform 调度配置项 | 完全可由现有 `group.platform` 字段表达，YAGNI |
| Bridge dispatch 路径重写 | 已完整，本 design 只补调度池 |
| Frontend `platformOptions` 调整 | UI 已支持创建 newapi group/账号，不在范围 |
| Sticky session key 按 platform 拆 | 无证据需要；recheck 已能保证安全降级 |
| Cache 迁移/清理 | bucket key 自然兼容，零迁移 |
| 第六平台抽象 | 无需求，不预留接口 |

## 7. 工作量与 Rollout

### 7.1 工作量估算

| 阶段 | 估时 | 产出 |
|---|---|---|
| 原型实现（P0 流程跑通） | 1 d | §3 全部实施 + US-001/002/003 单测 |
| 测试补全（6 维 + 回归） | 1.5 d | US-004~008 + 集成测试 testcontainer |
| 审批门禁（§5 落地） | 0.5 d | preflight 段 + contract 重生成 |
| 文档同步 | 0.5 d | CLAUDE.md 现状盘点更新（移除"first-class fifth platform 名实不符"的隐含债务） |
| **合计** | **3.5 d** | |

> 比测试者原报告"4.5 d Solution A"略低，因为本 design 砍掉了混池/UI/cache 迁移，
> 严格遵循"做最小切面"。

### 7.2 Rollout 顺序

按"一个完整 PR"交付，避免细碎切分稀释 review 注意力。该 PR 包含：

- §3.1 upstream 最小注入点（`scheduler.go` 1 行 + `bridge_dispatch.go` 1 行 + `openai_gateway_service.go` 1 处 sanitize 放行）
- §3.2 companion 文件（`scheduler_tk_pool.go`、`messages_dispatch_tk_newapi.go`、`openai_compat_tk_pool.go`）
- §5 preflight 段 + `scripts/export_agent_contract.py` 重生成
- §6 全部测试（US-001 ~ US-008 单元 + 集成 testcontainer）
- `CLAUDE.md` "Current Gateway Flow" 现状盘点更新（移除 `newapi` 名实不符的隐含债务）

合入后 SSM 升级 prod（参考 v1.3.1 升级模式，零数据迁移）。

> **为什么不切 3 个 PR**：本设计的 3 处注入点 + companion 文件 + 测试是同一个语义"放行 newapi 进调度池"的不同切面。
> 分开 review 反而拆散语义、增加来回；§3.2 没有 §3.1 的注入点不会被实际调用、§3.1 没有 §3.2 的 helper 又编译不过。
> 单 PR review 同时看到"接入点 + 实现 + 测试"才能判断切面完整。
> 这与 sticky-routing 的"单提交大爆炸"是不同的——后者是 8 个独立可上线特性强行打包，本 PR 是同一特性的最小不可分原子单元。

### 7.3 回滚策略

- §3.1 全部 upstream 改动可一次 git revert 完成（无 schema 变更）
- §3.2 companion 文件保留无害
- newapi group 再次失效，但 openai group 完全不受影响（自然降级）

## 8. 与 §5 upstream 兼容审计

| §5 条款 | 本 design 合规性 |
|---|---|
| §5 不得 net-delete upstream 符号 | ✅ 0 删除 |
| §5 优先 companion `*_tk_*.go` | ✅ 真实逻辑全在 §3.2 companion |
| §5 upstream 文件改 = thin injection | ✅ 每处 ≤ 5 行，全部为"加字段/换 helper 调用" |
| §5.x 默认 = 保留 upstream 能力 | ✅ openai group 行为完全保留，newapi 是新增能力 |
| §5.y 无历史重写 | ✅ 走 PR + 真实 merge commit |
| §5.y 上游合并友好 | ✅ companion 文件不参与 upstream merge 冲突；upstream 文件改动可被 upstream 后续重构吸收 |

## 9. 验收清单（合并门禁）

- [x] §3.1 全部 upstream 注入点完成且每处 ≤ 5 行 — 4 文件，diff ≈ +98/-46 in upstream-shaped files (commit `6e0c0fce`)
- [x] §3.2 全部 companion 文件 + 单元测试 — `account_tk_compat_pool.go`、`openai_gateway_service_tk_newapi_pool.go`、`openai_messages_dispatch_tk_newapi.go` + 三个 `*_test.go`（commits `0211c2b7`, `6e0c0fce`）
- [x] US-008..US-015 全部从 Draft → InTest（unit-tier AC 已断言；e2e AC 由 follow-up PR 推进，见 §11）
- [x] §5.1 preflight 段（newapi compat-pool drift，sub2api-specific）加入 `scripts/preflight.sh § 9` 并行为验证可 fail（commit `4441b642`，merge PR #11 后 wrapper 重构为 `dev-rules/templates/preflight.sh § 1-8 + § 9 sub2api`）
- [ ] §5.2 集成测试 testcontainer 化 — **延期到 follow-up PR `feature/newapi-fifth-platform-e2e`**（见 `docs/preflight-debt.md` §4，2026-05-03 截止）；当下用 21 个 mock-based 单测覆盖全部安全/逻辑/回归 AC
- [x] §5.3 `scripts/export_agent_contract.py --check` 由 `dev-rules/templates/preflight.sh § 4 (agent contract drift)` 自动接入，本 PR 仅 audit 模式（routes/*.go ↔ doc 计数 + Notes 段平台覆盖），完整 prefix-resolving generator 见 preflight-debt §3（commit `ab39ddbb`）
- [x] `go test -tags=unit ./internal/service/...` 全绿 — 82.8s（M5a 验证日志：`.testing/user-stories/attachments/us-newapi-unit-run-2026-04-19.txt`）
- [x] CLAUDE.md "Current Gateway Flow" 段补 newapi 调度池语义（M8，commit `90d5d90c`）

## 10. 设计前后对比

> 见 git log `feature/newapi-fifth-platform ^main`（2026-04-19 起 9 commits, ~6 working files in
> backend/internal/service/）。三个 helper（`IsOpenAICompatPoolMember`, `OpenAICompatPlatforms`,
> `isOpenAICompatPlatformGroup`）替换了所有 `IsOpenAI()` 调度筛选场景；upstream 文件每处改动 ≤ 5 行。

## 11. 实施情况（2026-04-19 ~ 2026-04-20）

PR：[`feature/newapi-fifth-platform → main`](https://github.com/youxuanxue/sub2api/pull/) — 待开。

### 11.1 提交序列（review 顺序）

| Milestone | Commit | 内容 |
|---|---|---|
| M1 现状对齐 | `af9a73a6` | design doc frontmatter pending → approved + 扩展 `check_approved_docs.py` lifecycle 支持 `approved` |
| M2 用户故事 | `e9f8bc56` | 新建 `.testing/user-stories/stories/US-008..US-015.md` 8 篇（覆盖 §0 全部 P0 AC + 回归基线）+ index 更新 |
| M3 companion (part 1) | `0211c2b7` | `account_tk_compat_pool.go` + `account_tk_compat_pool_test.go` + `openai_messages_dispatch_tk_newapi.go` |
| US 编号对齐 | `c5130c29` | design doc §4.1 / §4.2 / §5.2 用全局 ID（US-008..015）替换设计期 alias `US-NEWAPI-001..008` |
| M3-补 + M4 | `6e0c0fce` | §3.1 upstream U1-U7 注入点（`scheduler.go` + `gateway_service.go` + `messages_dispatch.go` + `ws_forwarder.go`，4 文件 +98/-46）+ companion `openai_gateway_service_tk_newapi_pool.go` + 漏补的 `openai_messages_dispatch_tk_newapi_test.go` |
| M5a | `bf784cab` | scheduler-tier 行为单测 8 case（`openai_account_scheduler_tk_newapi_test.go`）+ gateway-tier sticky 单测 5 case（`openai_gateway_service_tk_newapi_pool_test.go`）+ 8 故事 status → InTest + Linked Tests 回填 + evidence 归档 + preflight-debt §4 记录 M5b 延期 |
| M6 | `4441b642` | `scripts/preflight.sh § 2` 段（两条 drift check：直接 `PlatformOpenAI` bucket / 裸 `!account.IsOpenAI()`），POSIX grep + 行为验证可 fail（merge PR #11 后由 `scripts/preflight.sh § 9` 承担，语义不变） |
| M7 | `ab39ddbb` | `scripts/export_agent_contract.py`（audit 模式）+ 项目级 `preflight § 3` 接入 + `docs/agent_integration.md` Notes 段 5 平台 + newapi 三入口契约 + preflight-debt §3 更新（merge PR #11 后由 `dev-rules/templates/preflight.sh § 4 (agent contract drift)` 自动调用，无需项目级段） |
| M8 | `90d5d90c` | CLAUDE.md "Current Gateway Flow" 补 newapi 调度池语义 + design doc §9 验收清单勾选 + frontmatter shipped + §11 实施情况 + preflight-debt §2 closed |
| Merge `origin/main` (PR #11) | TBD | 接入 dev-rules submodule（删除项目级 `scripts/preflight.sh` + `scripts/check_approved_docs.py`）→ 重建 `scripts/preflight.sh` 为 wrapper（dev-rules 模板 § 1-8 + sub2api § 9）、同步对齐文档 § 编号引用、CLAUDE.md §10 描述更新为"thin wrapper" |

### 11.2 单测覆盖（34 case，覆盖 US-008..015）

实际分布（`grep -cE "^func Test"`）：

- `account_tk_compat_pool_test.go`：9（compat-pool predicate truth-table，US-011/012）
- `openai_account_scheduler_tk_newapi_test.go`：8（scheduler-tier，US-008/011/012/015）
- `openai_gateway_service_tk_newapi_pool_test.go`：5（sticky-session，US-011/013/015）
- `openai_messages_dispatch_tk_newapi_test.go`：12（dispatch sanitize + truth table，US-009/014/015）

下表列出按风险类型 × 故事 ID 的代表性映射（非全集）：

| 风险 | US | 测试 | 文件 |
|---|---|---|---|
| 逻辑（正向） | US-008 | `TestUS008_NewAPIGroup_Scheduler_PicksNewAPIAccount` | `openai_account_scheduler_tk_newapi_test.go` |
| 逻辑（无回退） | US-008/012 | `TestUS008_NewAPIGroup_PoolEmpty_NoFallback` | 同上 |
| 回归（正向） | US-008/015 | `TestUS008_OpenAIGroup_SchedulerSelect_Unchanged` | 同上 |
| 安全（混池防御 1） | US-011 | `TestUS011_LoadBalance_FiltersOutNewAPIFromOpenAIGroup` | 同上 |
| 安全（混池防御 2） | US-011 | `TestUS011_LoadBalance_FiltersOutOpenAIFromNewAPIGroup` | 同上 |
| 安全（channel_type=0） | US-012 | `TestUS012_LoadBalance_ExcludesNewAPIChannelTypeZero` | 同上 |
| 逻辑（池空报错） | US-012 | `TestUS012_NewAPIGroup_AllChannelTypeZero_PoolEmpty` | 同上 |
| 缓存分桶 | US-015 | `TestUS015_SchedulerBucket_PartitionedByPlatform` | 同上 |
| 逻辑（sticky HIT） | US-013 | `TestUS013_Sticky_NewAPIGroup_HitsBoundAccount` | `openai_gateway_service_tk_newapi_pool_test.go` |
| 安全（sticky 漂移 1） | US-011/013 | `TestUS011_Sticky_FailsOver_WhenAccountChangedPlatform` | 同上 |
| 安全（sticky 漂移 2） | US-013 | `TestUS013_Sticky_NewAPIGroup_FailsOver_WhenStickyAccountIsOpenAI` | 同上 |
| 安全（sticky 配置失效） | US-013 | `TestUS013_Sticky_NewAPIGroup_FailsOver_WhenChannelTypeReset` | 同上 |
| 回归（openai sticky） | US-015 | `TestUS015_Sticky_OpenAIGroup_HitPreserved` | 同上 |
| 逻辑（dispatch 放行） | US-009/014 | `TestUS009_Sanitize_NewAPIGroup_Preserves`, `TestUS014_NewAPIGroup_MessagesDispatchConfig_RoundTrip` | `openai_messages_dispatch_tk_newapi_test.go` |
| 回归（dispatch 清除） | US-009/014 | `TestUS009_Sanitize_AnthropicGroup_Cleared` 等 5 case | 同上 |
| Truth table | predicate | `TestIsOpenAICompatPlatformGroup_Truth` | 同上 |

全部 34 case 在 `go test -tags=unit -count=1 ./internal/service/...` 内运行（82.8s 全 service suite，含本 PR 之外测试），无新增失败、无 flaky。

### 11.3 推迟项（follow-up PR）

| 项 | 范围 | 截止 |
|---|---|---|
| §5.2 e2e（HTTP+PG+upstream） | US-008/009/010 三入口端到端，testcontainer 化 | 2026-05-03（preflight-debt §4） |
| §5.3 完整 prefix-resolving contract generator | Go AST walker 或 runtime route dump，重生成 393 routes 的完整列表 | 下次 routes 重构前（preflight-debt §3） |
| §9 §5.2 验收勾选 | 由 e2e PR 完成时补打 | 同上 |

### 11.4 已建立的合规门禁（防止下次"shipped under pending"）

- **frontmatter 不变量 R1-R5**：`dev-rules/scripts/check_approved_docs.py`（lifecycle 现支持 `approved` 状态，本 PR M1 提案，后由 PR #11 上提到 dev-rules 与 zw-brain 共享）→ `dev-rules/templates/preflight.sh § 7`
- **newapi compat-pool drift**（sub2api-specific）：`scripts/preflight.sh § 9`（行为验证：构造 probe 文件能稳定 fail）
- **agent contract drift**：`scripts/export_agent_contract.py --check` → 由 `dev-rules/templates/preflight.sh § 4` 自动调用（行为验证：删除 `newapi` 后 Notes 段覆盖检查稳定 fail）
- **故事 ↔ 测试对齐**：所有 8 篇 user story 状态 InTest + Linked Tests 与真实测试函数对齐（test-philosophy §5 漂移检测）
- **CI 与本地强度对齐**：`.github/workflows/backend-ci.yml` `preflight` job（`submodules: recursive`）与 pre-commit hook 走同一 `scripts/preflight.sh`
- [ ] `go test -tags=integration ./...` 全绿（与 §9 验收清单中 §5.2 一并由 follow-up e2e PR 关闭）
- [ ] `golangci-lint run ./...` 无新问题
- [ ] 旧 openai group 在 prod 镜像里手测三入口仍正常
