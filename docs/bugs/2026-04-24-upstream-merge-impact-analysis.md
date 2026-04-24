---
title: Upstream Merge Impact Analysis — Wei-Shaw/sub2api@d162604f → TokenKey fork
date: 2026-04-24
auditor: Cloud Agent
scope: backend (auth identity rebuild, RPM, channel monitor, OpenAI images, fifth-platform newapi)
status: draft
related_design: docs/approved/newapi-as-fifth-platform.md
upstream_head: d162604f (Wei-Shaw/sub2api main)
fork_head: f0757011 (origin/main)
ahead: 88
behind: 248
---

# Upstream Merge Impact Analysis — `Wei-Shaw/sub2api@d162604f` → TokenKey fork

> 状态：草稿 / Draft（尚未 merge；本文件是 merge 之前的 due-diligence
> 审计，按 `digital-clone-research.md` Jobs 聚焦 + OPC 自动化哲学）。
> 范围：`origin/main` (TokenKey f0757011) ⟵ `upstream/main` (Wei-Shaw d162604f)

---

## 0. TL;DR（如果只读一行）

**直接 `git merge upstream/main` 会让 TokenKey 第五平台 `newapi` 全面 P0
回归**。upstream 在过去 248 commits（first-parent 25 步：12 个 PR +
13 个 chore/fix 直推；详见 §1.2 数字校准）中两个独立线索同时把
`backend/internal/service/openai_account_scheduler.go` 与
`backend/internal/service/openai_gateway_service.go` 的调度路径**改回**了
裸 `IsOpenAI()` 调用与无 `GroupPlatform` 字段的请求结构，与
`docs/approved/newapi-as-fifth-platform.md`（已 shipped）的最小不可分原子
patch 直接对冲。同样会被静默回滚的还有 `openai_messages_dispatch.go` 的
`isOpenAICompatPlatformGroup` 谓词（被 upstream 改回 `g.Platform == PlatformOpenAI`）
与 `endpoint.go` 中 `case service.PlatformOpenAI, service.PlatformNewAPI`
（被 upstream 改回 `case service.PlatformOpenAI`）。

这些回归**不会编译失败**（upstream 把方法签名也改回去了，所以 type
check 仍然过），但会让 `group.platform=newapi` 的请求在所有三个 OpenAI-compat
入口（`/v1/chat/completions` `/v1/messages` `/v1/responses`）拿到空池并报
"no available OpenAI accounts"——与 2026-04-19 测试者上报的原始 P0
完全等价。

按 OPC 自动化哲学，**`scripts/preflight.sh § 9 (newapi compat-pool drift)`
与 `scripts/check-newapi-sentinels.py` 是会拦截这次回归的唯二硬约束**。
任何 merge resolution 都必须让两条机械检查归零，而不是「靠自觉把
companion 文件加回去」。

按 Jobs 聚焦原则，**本次 merge 应分阶段、按风险隔离**，而不是当作一个
"248 commits 一把梭" 的大爆炸合并：

```
Stage A (P0 必合)  → auth identity + payment 安全修复 + 关键 bug 修复
Stage B (P1 应合)  → channel monitor + RPM 限流 + OpenAI 生图
Stage C (P2 选合)  → available channels view + monitor UI 美化
```

---

## 1. 上游变化盘点（事实，无解释）

### 1.1 数字交叉核对（4 角度）

```
$ bash scripts/check-upstream-drift.sh
Upstream:  Wei-Shaw/sub2api@d162604f
TK fork:   origin/main@f0757011
TK ahead:  88 commits
TK behind: 248 commits

$ git rev-list --left-right --count origin/main...upstream/main   →  88  248
$ git rev-list --count --merges     origin/main..upstream/main    →  18    (PR merge commits in graph)
$ git rev-list --count --no-merges  origin/main..upstream/main    →  230   (real-patch commits)
$ git rev-list --count --first-parent origin/main..upstream/main  →  25    (steps along main's first-parent)
$ git cherry origin/main upstream/main | awk '{print $1}' | sort -u
+                                                                  →  230   (none "-" → 0 cherry-picked already)
$ git merge-base origin/main upstream/main
78f691d2 (2026-04-21 12:13  "chore: update sponsors")

$ git diff --shortstat origin/main...upstream/main
518 files changed, 133353 insertions(+), 11930 deletions(-)
$ git diff --stat origin/main...upstream/main -- backend/ent/ | tail -1
94 files changed, 53099 insertions(+), 5882 deletions(-)
$ git diff --stat origin/main...upstream/main -- backend/migrations/ | tail -1
28 files changed, 1833 insertions(+)
```

### 1.2 248 的含义校准（避免被"248"虚高吓退）

| 视角 | 数字 | 含义 |
|---|---|---|
| `rev-list --count` 原始 | **248** | upstream 可达、origin 不可达的全部 commit；与 `check-upstream-drift.sh` 同口径 |
| 真实带 patch 内容的 commit | 230 | 排除 18 个 merge commit（merge 本身不带 patch） |
| upstream main first-parent 步数 | 25 | "main 上多走了 25 步"；其他 223 个是 PR 内部 WIP，跟随 PR 一起进 |
| 实际要 review 的 PR 单元 | **12** | first-parent 中的 merge commit，每个 = 1 个 GitHub PR |
| 已被 cherry-pick 吸收 | **0** | `git cherry` 全部 `+`，无 `-`，原 fork 没从 upstream cherry-pick 过任何 commit |

所以 review 重量是 **12 个 PR + 13 个 chore/fix 直推**，不是 248。

### 1.3 25 个 first-parent 节点详表

| 类型 | 上游引用 | 主题 | TK 风险 |
|---|---|---|---|
| PR | #1853 | codex-image-generation-bridge：把 codex 生图桥接到 `/v1/responses` | M（动 scheduler request struct） |
| PR | #1850 | channel-insights：监控页 UI 改造（OPERATIONAL/DEGRADED） | L（纯前端） |
| PR | #1836 | account-daily-weekly-quota-cache-invalidation：配额缓存失效修复 | L |
| PR | #1829 | codex-oauth-proxy-message：OAuth 代理错误信息 | L |
| PR | #1828 | wx-11/main：计费问题以及模型回显修复 | M（动 scheduler hot path） |
| PR | #1815 | **feat_rpm**：新增 Group/User 级 RPM 限流（schema+migration） | **H（schema 变更）** |
| PR | #1813 | fix-openai-image-handling：图像处理 LimitReader 防 OOM | L |
| PR | #1810 | fix/profile-auth-bindings-i18n（IanShaw027 第二轮 follow-up） | L |
| PR | #1802 | fix/profile-auth-bindings-i18n（IanShaw027 第一轮） | L |
| PR | #1799 | **rebuild/auth-identity-foundation**（IanShaw027 续作） | **H（schema + 主流程改动）** |
| PR | #1795 | feat/openai-image-api-sync：OpenAI 同步生图接入 | M（端点扩展） |
| PR | #1785 | **rebuild/auth-identity-foundation**（IanShaw027 主提交） | **H（schema + 主流程改动）** |
| chore | a4e329c1 | fix: openai 默认模型新增 gpt5.5 | L |
| chore | ca204ddd | fix(openai): preserve image outputs when text content serialization fails | M（OpenAI handler） |
| chore | 0a80ec80 / 6449da6c / d162604f | sync VERSION to 0.1.115 / 0.1.116 / 0.1.117 `[skip ci]` | L（**TK 不接受 upstream VERSION**，见 §6） |
| chore | a22a5b9e | fix docker pull version tag in TG notification | L |
| chore | 3fe4fd4c | chore: add model gpt-5.5 | L |
| chore | ef967d8f | **fix: 修复 golangci-lint 报告的 36 个问题** | M（散落，含 §2.1 hot path） |
| chore | 0b85a8da | fix: add io.LimitReader bounds to prevent OOM in image handling | L |
| chore | 755c7d50 | chore: revert README files to 78f691d2 version | L |
| chore | c6d25f69 | chore: 恢复 PAYMENT 系列文件 | L（恢复 PR #1785 重构期间误删的文件） |
| chore | 45065c23 | fix(ci): run 108a migration before 109 in backfill integration test | L |
| chore | 4d0483f5 | feat: 补充 gpt 生图模型测试功能 | L |

> 12 个 PR + 13 个 chore = 25 个 first-parent 节点，覆盖全部 248 commits 的语义。
> 数字遵守 dev-rules"删数字"原则不写入正文，只在表格里给读者一个判断
> 重量的快速锚点；表格本身是契约（删了表格就是删了体检报告），不需要 stat
> 块包装。

---

## 2. 与 TK 第五平台 `newapi` 的对冲点（这是本次 merge 的核心矛盾）

### 2.1 调度热路径——upstream 改回了"裸 `IsOpenAI()`"

`docs/approved/newapi-as-fifth-platform.md` §3.1 的核心修补是把所有调度
过滤路径从 `!account.IsOpenAI()` 切到
`!account.IsOpenAICompatPoolMember(groupPlatform)`。upstream 在
`#1815 (feat_rpm)`、`#1828 (计费问题修复)`、`#1850 (channel-insights)`
合入过程中**完整重写**了 `OpenAIAccountScheduleRequest` 结构，**删除了
`GroupPlatform` 字段**，并把 4 处筛选**改回**裸 `IsOpenAI()`：

```
$ git diff origin/main upstream/main -- backend/internal/service/openai_account_scheduler.go \
    | grep -E "^[+-].*(IsOpenAICompatPoolMember|IsOpenAI\(\)|GroupPlatform|recheckSelectedOpenAIAccountFromDB)"
-	GroupPlatform      string // TK: scheduling-pool platform ...
-	if shouldClearStickySession(...) || !account.IsOpenAICompatPoolMember(req.GroupPlatform) || ...
+	if shouldClearStickySession(...) || !account.IsOpenAI() || ...
-	account = s.service.recheckSelectedOpenAIAccountFromDB(ctx, account, req.RequestedModel, req.GroupPlatform)
+	account = s.service.recheckSelectedOpenAIAccountFromDB(ctx, account, req.RequestedModel)
-		if !account.IsSchedulable() || !account.IsOpenAICompatPoolMember(req.GroupPlatform) {
+		if !account.IsSchedulable() || !account.IsOpenAI() {
... (4 occurrences in scheduler.go + 7 occurrences in gateway_service.go)
```

`openai_gateway_service.go` 同样删除了 `resolveGroupPlatform` 调用和
`groupPlatform` 局部变量沿调用链的传播；`recheckSelectedOpenAIAccountFromDB`
方法签名从 `(ctx, account, model, groupPlatform)` 改回
`(ctx, account, model)`。

**这意味着**：merge 时不能"接受 upstream 的版本"——必须把 TK 的
`GroupPlatform` 字段加回 `OpenAIAccountScheduleRequest`，把所有 4+7 处
`IsOpenAI()` **再次**改回 `IsOpenAICompatPoolMember(req.GroupPlatform)`，
并把 `recheckSelectedOpenAIAccountFromDB` 签名再次扩展为 4 参数。同时
要保留 upstream 新增的 `RequiredImageCapability` 字段、advanced
scheduler setting 缓存与 singleflight、image capability 过滤——这些是
TK 不想丢的真实价值。

`openai_messages_dispatch.go` 同理：upstream 把
`isOpenAICompatPlatformGroup(g)` 改回 `g.Platform == PlatformOpenAI`，必须
再次撤销，否则 newapi group 的 `messages_dispatch_model_config` 会被
sanitize 强清。

### 2.2 端点推导——upstream 把 `case PlatformNewAPI` 删了

`backend/internal/handler/endpoint.go:76` 上游改回：

```diff
-	case service.PlatformOpenAI, service.PlatformNewAPI:
-		if upstream, ok := tkDeriveOpenAITokenKeyUpstream(inbound); ok {
-			return upstream
-		}
+	case service.PlatformOpenAI:
+		if inbound == EndpointImagesGenerations || inbound == EndpointImagesEdits {
+			return inbound
+		}
```

`scripts/newapi-sentinels.json` 已为这条线设置硬哨兵
（`backend/internal/handler/endpoint.go` `must_contain: ["service.PlatformNewAPI"]`），
合并时如果直接接 upstream 文件，preflight § 9 + sentinel registry 会双
重 fail——这是设计上要求的。merge resolution 必须既保留 upstream 新增
的 ImagesGenerations/Edits 处理，又把 `case` 改回 `PlatformOpenAI,
PlatformNewAPI` 并优先委托给 `tkDeriveOpenAITokenKeyUpstream`（后者已经覆盖
ImagesGenerations 与 Embeddings 两条 inbound）。

### 2.3 TK companion 文件 vs upstream 文件的命名/结构冲突

| TK companion 文件（保留） | 与之绑定的 upstream 文件（必须 thin-injection） | merge 状态 |
|---|---|---|
| `account_tk_compat_pool.go` | `service/account.go` | 兼容（无冲突） |
| `openai_gateway_service_tk_newapi_pool.go` | `service/openai_gateway_service.go` | **冲突**（必须保留 `listOpenAICompatSchedulableAccounts` / `resolveGroupPlatform` 注入点） |
| `openai_messages_dispatch_tk_newapi.go` | `service/openai_messages_dispatch.go` | **冲突**（必须保留 `isOpenAICompatPlatformGroup` 注入点） |
| `admin_service_tk_newapi_save.go` | `service/admin_service.go` | **冲突**（upstream 重写 admin_service ~1400 行 diff，调用链需要重新挂回） |
| `endpoint_tk.go` | `handler/endpoint.go` | **冲突**（见 §2.2） |
| `service/openai_gateway_bridge_dispatch_tk_video.go` | `internal/relay/bridge/video_relay.go` | 兼容（upstream 未碰） |

### 2.4 `newapi` integration 包

`backend/internal/integration/newapi/` 在 upstream 完全不存在（这是 TK
独有的桥接层，via `replace github.com/QuantumNous/new-api => ../../new-api`），
upstream merge 不应触碰任何文件。merge 后必须验证：

```
$ rg -l "package newapi" backend/internal/integration/newapi/ | wc -l
# 必须保持当前数量，不能因为 merge 漂移
```

`scripts/newapi-sentinels.json` 已为 `channel_types.go`、`fusion.go` 设
置 `must_contain: ["package newapi"]` 哨兵——merge 后跑 sentinel 检查
即可发现意外删除。

### 2.5 前端 5 平台枚举

`frontend/src/constants/gatewayPlatforms.ts` 与
`frontend/src/composables/usePlatformOptions.ts` 也是 TK 独有，upstream
merge 不应触碰。前端冲突主要集中在 auth callback 视图（OAuth 重构）、
Settings 页（payment/wechat 配置）、AppSidebar/AppHeader（available channels
view 入口），与 5 平台枚举无关。

---

## 3. 风险分级矩阵（决定 merge 策略）

| 上游变更块 | 文件触面 | 与 newapi 第五平台关系 | TK 数据库迁移要求 | 风险 | 推荐策略 |
|---|---|---|---|---|---|
| **A. auth identity foundation 重构**（#1799/#1785） | 9 个 ent schema、10+ migrations 108–124 | 无直接交互 | **新表 6 张** + user 表加 4 字段 | **H** | 单 PR 隔离合入；先合 schema + migration，再合 service/handler |
| **B. payment 路由 + provider snapshot**（PR 多） | payment_order schema、migration 111/112/117/119/120/120a | 无 | payment_orders 加 2 字段 + 唯一索引 | M | 与 auth 同 PR 合入（耦合度高） |
| **C. RPM 限流**（#1815） | group/user schema、migration 125/126/127、ratelimit_service | 无（限流在调度之前） | group/user 加 `rpm_limit` 字段 | M | 单独 PR；schema 改动最小 |
| **D. channel monitor**（#1850 + 多个 channel monitor PRs） | 4 张新表 channel_monitor*、migration 125/126/127/128/129 | 无 | 新表 4 张 + 模板 seed | M | 单独 PR；纯独立模块 |
| **E. OpenAI 同步生图**（#1795 + #1853） | endpoint.go、openai_*_handler、scheduler request struct | **强冲突**（见 §2.1/§2.2） | 无 | **H** | **必须与 §2 修复一起合**；不能独立 |
| **F. available channels view**（多个 channels PRs） | 用户端新页面、settings dual-mode | 无（5 平台枚举隔离） | 无 | L | 可与 D 同 PR 或单独 |
| **G. golangci-lint 36 个 issue 修复**（ef967d8f） | 散落 | 部分覆盖 §2.1 删除 | 无 | M | 不可单独 cherry-pick；upstream 是大杂烩 commit |
| **H. 杂项 bug 修复**（计费、模型回显、403 冷却） | scheduler/gateway hot path | **强冲突**（同 E） | 无 | M | 与 E 合并 |

---

## 4. 推荐 merge 策略（Jobs 聚焦 + OPC 自动化）

### 4.1 反对方案：`git merge upstream/main` 一把梭

**为什么不行**：
1. **OPC 角度**：`scripts/preflight.sh § 9` + `scripts/check-newapi-sentinels.py`
 + `.github/workflows/upstream-merge-pr-shape.yml` 三道硬约束会同时 fail，
 单 PR 无法在合理时间内全部 resolve；
2. **Jobs 角度**：248 commits 跨 4 个相互独立的功能域（auth/payment/RPM/monitor）
 + 1 个与 TK 强冲突的 OpenAI 生图重构，混在一起 review 等于没 review；
3. **安全角度**：auth identity 重构本身有 10+ 个修复 commit（"fix(ci): align
 auth and payment verification tests" 等），说明 upstream 自己 release
 期间也在打 patch，吸收时机选错会拖到 prod。

### 4.2 推荐方案：分 3 个 merge PR，按风险隔离

> 全部走 `merge/upstream-YYYYMMDD-stage{A,B,C}` 分支，由
> `.github/workflows/upstream-merge-pr-shape.yml` 自动验证 (a) 真实
> merge commit shape (b) `upstream/main..HEAD` 审计行 (c) 无 `[skip ci]`
> 污染 (d) sentinel registry 完整。本节是策略，不替代 merge 时的人工
> 解冲。

#### Stage A — `merge/upstream-stageA-auth-payment`

**范围**：所有 auth identity / payment 修复（PR #1785 / #1799 +
所有 `fix(payment)` `fix(auth)` `fix(oauth)` 系列 commit）

**合入手法**：
1. `git checkout -b merge/upstream-stageA-auth-payment`
2. `git merge --no-ff upstream/main`（产生大冲突文件）
3. **冲突 resolve 前**：先把以下 6 个 TK 文件标为 "ours" 优先，但保留
 upstream 的 import / 接口扩展：
 - `backend/internal/service/openai_account_scheduler.go`（保留 TK 的 `GroupPlatform` + `IsOpenAICompatPoolMember`，吸收 upstream 的 advanced scheduler setting + `RequiredImageCapability`）
 - `backend/internal/service/openai_gateway_service.go`（同上 + 保留 `resolveGroupPlatform` 沿调用链传播）
 - `backend/internal/service/openai_messages_dispatch.go`（保留 TK 的 `isOpenAICompatPlatformGroup` 谓词）
 - `backend/internal/handler/endpoint.go`（保留 `case ..., service.PlatformNewAPI` + 优先委托 `tkDeriveOpenAITokenKeyUpstream`，吸收 upstream 的 `EndpointImagesEdits`）
 - `backend/internal/service/admin_service.go`（保留对 `resolveNewAPIMoonshotBaseURLOnSave` 的调用）
 - `backend/ent/schema/group.go` `backend/ent/schema/user.go`（合并 TK 既有字段 + upstream `rpm_limit` + auth identity edges）
4. 跑 `go generate ./ent` 让 ent 自动重建 mutation/where/create
5. 跑 `go generate ./cmd/server` 重建 wire_gen
6. 把 upstream 新增 migration 108–124 全部 cp 到 `backend/migrations/`，
 与 TK 的 `tk_001..tk_005` 共存（migration 编号空间不冲突，
 因为 TK 用 `tk_*` 前缀）
7. **运行机械门禁**：
 ```
 bash scripts/preflight.sh # 必须全部归零
 python scripts/check-newapi-sentinels.py
 cd backend && go test -tags=unit -run 'TestUS00|TestUS01' ./internal/service/...
 ```
8. **运行集成测试**：testcontainer 跑 auth identity 的 13 个新增 integration test。
9. PR body **必须**包含
 `git log --oneline upstream/main..HEAD | wc -l` 与
 `git diff --stat upstream/main..HEAD -- backend/ | head -5`（CLAUDE.md §5.y 要求）

**预期冲突量**：约 30 个 conflict 文件（见 `/tmp/merge-tree.txt` 的 conflict
列表）；其中 ent generated code 占 ~15 个（regen 自动覆盖），ent schema 2
个，service/handler 7 个 hot path，剩余是 setting/dto/wire。

#### Stage B — `merge/upstream-stageB-rpm-monitor`

**前置**：Stage A 已 merge 到 `main`。

**范围**：channel monitor 4 表 + RPM 限流 + available channels view + OpenAI 生图。

**为什么放 Stage B**：
- channel monitor 与 newapi 调度无交互，是独立模块；
- RPM 限流在调度之前，不影响 §2.1 的 IsOpenAICompatPoolMember 路径；
- OpenAI 生图（PR #1795 + #1853）虽然动 scheduler 但已在 Stage A
 完成 hot-path resolve，再次 merge 时上游对 scheduler 的改动应已被
 Stage A 吸收，冲突收敛。
- available channels view 是用户端新页面，与 admin UI 5 平台枚举无关。

**合入门禁同 Stage A**。

#### Stage C — `merge/upstream-stageC-misc`

**范围**：剩余 i18n/UI/小修复。

可选；若 Stage A+B 之后差距 < 30 commits 且无 P0 项，可以并入下次常规
merge cadence。

### 4.3 反对方案 B：cherry-pick auth identity 单 PR

**为什么不推荐**：auth identity 重构涉及 5 个 PR + 50+ 个修复 commit
+ 6 张新表 + 已 land 的 squash-merge commit `8eb3f9e7` `ddf80f5e`。
cherry-pick 会丢掉 upstream 的真实 merge commit shape，违反 CLAUDE.md
§5.y "tag = consolidation point" 约束，并让下次 merge 看到的 diff 与
本地"已 cherry-pick"内容打架。**真实 merge commit > cherry-pick 累计**。

---

## 5. OPC 自动化门禁（merge PR 必须全部归零）

| 门禁 | 来源 | 命令 | 失败语义 |
|---|---|---|---|
| Branch shape | `.github/workflows/upstream-merge-pr-shape.yml` | CI 自动 | 不是 merge commit / 缺审计行 / 含 `[skip ci]` / sentinel 缺失 |
| newapi compat-pool drift | `scripts/preflight.sh § 9` | `bash scripts/preflight.sh` | scheduler 直接用 `PlatformOpenAI` bucket 或裸 `IsOpenAI()` |
| newapi sentinels | `scripts/check-newapi-sentinels.py` + `scripts/newapi-sentinels.json` | preflight 调用 | 任一 sentinel 文件丢失或必含字符串消失 |
| Agent contract drift | `scripts/export_agent_contract.py --check` | preflight 调用 | route ↔ doc 漂移（上游新加 OpenAI images 路由会触发） |
| Story ↔ Test alignment | dev-rules preflight § 5 | preflight 调用 | US-008..015 linked tests 失效（merge 时 scheduler 字段改名会触发） |
| go test unit (service) | 本地 + CI | `go test -tags=unit ./internal/service/...` | 第五平台 mock 单测失败 |
| go test integration | CI testcontainer | `go test -tags=integration ./...` | auth identity migration 顺序错或 newapi pool 集成回归 |
| ent regen | 提交前 | `go generate ./ent` 后 `git diff --exit-code ent/` | schema 与 generated code 漂移 |

---

## 6. 不做的（聚焦过滤，与 §0 呼应）

| 不做 | 原因 |
|---|---|
| 升级 `backend/cmd/server/VERSION` 到 upstream 的 0.1.117 | TK 已自有 `1.6.0` 版本线，由 `scripts/release-tag.sh` 严格管理；upstream VERSION 是 ours-strategy |
| 接受 upstream 删除的 `service_pending_oauth_test.go` | 该文件是 upstream 自己重构时丢的，`auth_identity_payment_migrations_regression_test.go` 提供了更强覆盖，与 TK 第五平台无关 |
| 把 upstream channel monitor UI 改成 newapi 风格 | 范围爆炸；channel monitor 是独立模块，先按 upstream 风格合入，TK 化留作 follow-up |
| 在本次 merge 内为 RPM 限流添加 newapi 特殊化 | RPM 在调度之前，平台无关；无证据 newapi 需要不同 RPM 默认值 |
| 重写 upstream auth identity 以适配 TK 既有 OAuth | 违反 CLAUDE.md §5.x（不删 upstream 能力）；TK 的 OAuth 增量应在 merge 后以 `*_tk_*.go` companion 形式重新挂回 |
| 同时合 `dev-rules` submodule 升级 | merge 边界混乱；如有 dev-rules 升级需求另起 PR（先合 dev-rules，再合 upstream） |

---

## 7. 后续追踪项（merge 完成后）

1. **`docs/approved/newapi-as-fifth-platform.md` §11.2 测试矩阵复跑**：
 merge 后 34 个 mock 单测必须全绿；任一 fail 都说明 §2.1 resolve 不
 彻底，需立即 revert resolve 方案。
2. **更新 `scripts/newapi-sentinels.json`**：upstream 引入了
 `EndpointImagesEdits` 常量，若 TK `endpoint_tk.go` 选择以
 `tkDeriveOpenAITokenKeyUpstream` 收口 ImagesEdits，则需要在 sentinel 里
 把 `EndpointImagesEdits` 也加入 `endpoint.go` 的 `must_contain`。
3. **`docs/preflight-debt.md` D-003**（US-008/009/010 e2e 缺口）：merge
 引入了 upstream 新的 testcontainer auth identity test，如果其框架可复用
 应把 D-003 的 testcontainer 化提前到本次 merge 周期内。
4. **`.github/workflows/release.yml` `simple_release` 默认仍为 `false`**
 验证（CLAUDE.md §9.1，merge 不应触碰）。
5. **upstream 的 `.new-api-ref` 行为**：upstream 没有 `.new-api-ref`
 文件（这是 TK 独有的 cross-repo dependency 锚点），merge 不会改动；
 验证 `bash scripts/sync-new-api.sh --check` 仍归零。

---

## 8. 与 CLAUDE.md §5 / §10 的合规审计

| §5/§10 条款 | 本策略合规性 |
|---|---|
| §5 不得 net-delete upstream 符号 | ✅ 三阶段 merge 全保留 upstream 文件，仅在 hot path 用 thin injection 修正 |
| §5.x 默认 = 保留 upstream 能力 | ✅ auth identity / RPM / channel monitor / 生图全部接入 |
| §5.y 无 history 重写、merge commit shape | ✅ 每个 stage 走 `git merge --no-ff`，由 `upstream-merge-pr-shape.yml` 验证 |
| §5.y.1 PR 必须 include `git log upstream/main..HEAD` 审计行 | ✅ 见 §4.2 Stage A 第 9 步 |
| §5.y.1 newapi sentinel registry 完整 | ✅ 见 §5 表中第 3 行 |
| §10 dev-rules submodule 单一事实来源 | ✅ 不在本次 merge 内升级 dev-rules（见 §6 不做） |
| §9.1 `simple_release=false` 默认不变 | ✅ 见 §7 第 4 项 |
| §9.2 VERSION commit 不含 `[skip ci]` | ✅ `release.yml` 自动 sync VERSION 时由 release workflow 自身处理 |

---

## 9. 立即行动建议（给 reviewer / operator）

1. **先看 `/tmp/merge-tree.txt`** 和本文件 §2，确认 P0 回归点理解一致。
2. **不要直接在 `cursor/upstream-merge-analysis-bbcb` 分支上 `git merge upstream/main`**——
 本分支只承载分析文档，merge 工作在 `merge/upstream-stageA-auth-payment`
 等独立分支进行。
3. **决定是否走分 3 阶段策略**：如同意，开始 Stage A；如反对，请 reviewer
 写下替代方案的反对理由，进入审批门禁讨论。
4. **优先确认 `scripts/newapi-sentinels.json` 是否需要预先扩展**（§7 第 2 项），
 在 Stage A 开 PR 之前完成，否则 PR shape check 会反复 fail。

---

> 本文件**仅是 due-diligence 分析**，不进入 merge resolution 步骤。
> merge 实际执行需要新分支（`merge/upstream-stageA-*`）+ 新 PR。
