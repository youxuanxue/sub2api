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

按 Jobs 聚焦原则**反向问**："不合代价是什么？"——只有上游真正解决了
TK 用户已在踩的具体痛点，才值得付 merge 成本（详见 §3.5 价值分析）。

最小可执行方案（按 ROI 收敛）：

```
真正要合（P0 / 强 ROI）：
  Stage A    auth identity 重构 + payment provider snapshot + out_trade_no 唯一索引
             顺手吸收 OpenAI 同步生图（搭车 hot-path resolve，零边际成本）
  Stage B-1  group/user 维度 RPM 限流（前置验证与 TK 既有 quota 体系不重叠）

默认暂缓（弱 ROI / 待业务确认）：
  Stage B-2  channel monitor（4 表 + runner，仅当 TK 有渠道健康观测工单才合）
  Stage B-4  available channels view（仅当做 ToC 转化漏斗才合）
  Stage C    i18n / UI 微调 / 模型 ID 杂项（默认不合，gpt-5.5 model registry
             如有用户问可单独 cherry-pick 一行）
```

这把 review 重量从 "248 commits / 12 PRs" 收敛到 **2 个 merge PR**，
每个 PR 解决一个明确的 TK 用户痛点。**§4 的 3-stage 划分是"如果都要合"
的最大方案，§3.5 是"按真实价值挑"的最小方案——优先采用最小方案**。

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

## 3.5 价值分析（"是否值得合"——Jobs"做最少的事"反向问法）

§3 的风险矩阵回答了"合并要付什么代价"，但没回答"不合代价是什么"。
按 Jobs 聚焦原则，**默认不合**才是基线，必须由"上游真正解决了 TK 用户某个
具体痛点"来论证 merge 的必要性。下表对每个 stage 做这件事：

### 3.5.1 Stage A —— auth identity + payment 加固：**值得合，是 P0**

**TK 当前状态**（事实，从 `backend/internal/service/` 与 `backend/ent/schema/user.go` 实测）：

- TK 已有 LinuxDo OAuth (`auth_linuxdo_oauth.go`) + OIDC (`auth_oidc_oauth.go`)
 + Email 注册 + TOTP 二因素，但**没有**：
 - `AuthIdentity` 实体（多渠道身份共享一个 user 的统一抽象）
 - `PendingAuthSession`（OAuth 注册中途收集邮箱 / 绑定流程）
 - `IdentityAdoptionDecision`（重复登录身份漂移修复）
 - WeChat OAuth 登录（TK 只有 WxPay 微信支付，没有微信扫码登录）
- TK 已有 Stripe + Alipay + WxPay + EasyPay 四种 payment provider，
 但**没有**：
 - `payment_orders.provider_key` + `provider_snapshot`（订单创建时
 快照支付渠道凭证，防止运营改 secret 后历史订单 webhook 验签失败）
 - `out_trade_no` 唯一索引（重复 `out_trade_no` 当前可能产生
 双重发货）

**上游带来的价值**：

| 上游能力 | TK 缺口 | 实际用户价值 |
|---|---|---|
| `auth_identity` 表 + `pending_auth_session` 表 | 多渠道用户被强制按 email 唯一键合并，旧用户用 LinuxDo 注册后改 email 就被打成新人 | 解决重复账号工单（运营手动合并） |
| OAuth 注册中途绑邮箱（pending oauth flow） | OAuth 第一次登录如果对端没返回 email，TK 直接拒绝 | 直接挽回这部分新用户注册 |
| `payment_orders.provider_snapshot` | 改 Stripe webhook secret → 老订单 webhook 全部验签失败 → 客服救单 | 减少 P0 支付救单 |
| `out_trade_no` 唯一索引 | 高并发下 `out_trade_no` 碰撞 → 双重发货 → 运营退款 | 直接消除一类金额损失 |
| WeChat OAuth 登录 + 双模 wechat（公众号 / 网页授权） | TK 用户来自微信生态但只能用 email 注册 | 直接拓宽注册渠道（取决于 TK 是否做 ToC） |
| 122_pending_auth_completion_token_cleanup 等 5 条加固 migration | 无 | 防御边界，纯安全负债清理 |

**判断**：**值得**。这些是 TK 用户和运营**已经在踩**的坑（重复账号、支付救单、
微信生态用户拒之门外），且 TK 自己短期内不会重写一份等价 auth identity
框架——重写工作量 > 接受 upstream 的工作量。

**不合的代价**：(a) 客服救单成本持续；(b) 越往后 upstream 改动越多，下次
merge 的解冲突成本指数上升（上游已经在 main 上对 auth_identity 打了
50+ patch commit，再拖两个月这个数字会翻倍）。

**例外条款**：如果 TK 已经决定走"完全自研身份系统"路线（例如要接企业
SAML / 飞书 / 钉钉），那 Stage A 应该**只取 payment 修复**部分，
auth identity 整块用 `*_tk_*.go` companion 自研。需要业务侧确认这个分叉。

### 3.5.2 Stage B —— RPM + channel monitor + 同步生图：**部分值得**

**逐项判断**（不要把 Stage B 当成一个整体接受或拒绝）：

#### B-1 RPM 限流（#1815）—— **值得，但要核对是否与 TK 既有限流重复**

- 上游新增 `Group.rpm_limit` + `User.rpm_limit` 字段。
- TK 已有 `service/ratelimit_service.go` + `service/model_rate_limit.go`，
 当前限流靠 `quota.SharedRPM / FlashRPM / ProRPM`（按订阅档位），
 **没有 group / user 维度的兜底 RPM**。
- **价值**：当某个 group 内出现行为异常的 user（比如脚本刷流量），
 当前 TK 只能从全局 quota 维度限流，没法精准只压这个 user。
- **风险**：上游 RPM 限流 schema 与 TK 的 quota 体系是**叠加关系**还是
 **替代关系**？这必须在 merge 前确认；如果是叠加，可以直接合；如果是
 替代，需要写一个 migration 把 quota.SharedRPM 翻译到 user.rpm_limit。
- **判断**：值得合，但**列为 Stage B 内部第一优先级**，先验证语义。

#### B-2 channel monitor（#1850 + 多个 follow-up）—— **依赖业务场景**

- 上游新增 4 张表（`channel_monitors` / `channel_monitor_history` /
 `channel_monitor_daily_rollup` / `channel_monitor_request_templates`）+
 monitor scheduler runner + 用户端 dashboard。
- 上游 `channel_monitor.provider` enum 只有 `openai / anthropic / gemini`
 三个值——**没有 `newapi` 与 `antigravity`**。这是上游对 TK 五平台架构
 的盲区。
- **价值**：给 TK 运营一个"渠道是否健康"的可观测面板，目前 TK 是靠
 用户报障 + grafana 间接观察。
- **风险**：(a) 4 张新表 + 定时 runner，运行时成本不为 0；(b) provider
 enum 不含 `newapi`，合入后必须立即在 `*_tk_*.go` 扩展或在 schema
 外加 column——这是另一笔活；(c) 用户端 dashboard 的 UI 风格与 TK
 既有 admin UI 是否冲突需要前端 review。
- **判断**：**仅当 TK 当前确实有"渠道健康观测"工单**才合；否则推迟到
 业务确认。**不要因为"功能炫"就合**——这是典型的 Jobs 反例。

#### B-3 OpenAI 同步生图（#1795 + #1853）—— **值得，且强制要合**

- 上游新增 `/v1/images/generations` + `/v1/images/edits` 端点接入。
- TK `endpoint_tk.go` 已经有 `EndpointImagesGenerations` 常量与
 `tkDeriveOpenAITokenKeyUpstream` 派生路径——说明 TK 自己也在做这件事，
 但更早期、更简陋（只有 generations，没有 edits；只有 OpenAI，没有
 codex 走 `/v1/responses` 的桥接）。
- **价值**：让用户用 OpenAI image API。这是一个真实公开 API，不是
 实验性功能。
- **强制要合的原因**：上游对 scheduler request struct 的改动
 （`RequiredImageCapability` 字段 + `SupportsOpenAIImageCapability`
 谓词）会与 TK 的 `GroupPlatform` 字段同处一个 struct——一旦 Stage A
 解了 hot-path 冲突，B-3 几乎是免费搭车。
- **判断**：值得合，且**作为 Stage A merge 的第二个语义单元**而不是
 Stage B 独立 PR——它的冲突点已经在 Stage A 解决，独立合反而要再解
 一次同样的冲突。

#### B-4 available channels view（多个 channels PRs）—— **可选**

- 上游新增"用户端可用渠道"页面 + settings dual-mode + 平台分组的
 渠道聚合视图。
- TK 的 `gatewayPlatforms.ts` 已经定义了 5 平台枚举；上游这个 view
 在 4 平台前提下设计，需要在前端 `*.tk.ts` 扩展加上 `newapi` 平台
 段，否则用户端看到的"可用渠道"会缺一块。
- **价值**：用户端透明度，类似公网 status page。**取决于 TK 是否做 ToC
 转化漏斗**。
- **判断**：**默认不合**；如果 TK 用户增长团队明确要求"让用户看到
 可用渠道"再合。

### 3.5.3 Stage C —— i18n / UI polish / 杂项 bug：**默认不合**

- 范围：profile auth bindings i18n 修复、404 计费修复、监控页 UI
 微调（OPERATIONAL/DEGRADED 状态文案）、`gpt-5.5` 模型 ID 注册等。
- **价值**：单独看每条都很小；合在一起也是"小修小补"。
- **判断**：**默认不合**。等下次有更大主题 merge 时顺便带过来。
 单独为这些拉一个 PR + CI 周期的边际成本 > 收益。
 - **例外**：`a4e329c1 (gpt-5.5 模型新增)` 与 `3fe4fd4c (add model gpt-5.5)`
 如果 TK 已有用户问"gpt-5.5 为什么不能用"，可以单独 cherry-pick
 model registry 那一行（不需要 Stage C 整体）。
 - **不接受**：3 个 `chore: sync VERSION to 0.1.115/.116/.117 [skip ci]`
 commits 必须 ours-strategy 拒绝（CLAUDE.md §9.2 + TK 自己的 1.6.0
 版本线）。

### 3.5.4 总结：合并 ROI 表

| Stage | 上游内容是否解决 TK 当前真实工单 | TK 自研同等能力的工作量 | 合并工作量 | 推荐 |
|---|---|---|---|---|
| A.auth | 是（重复账号 / OAuth 拒注册 / 支付救单 / 双重发货） | 高（重写身份层） | 中（schema + hot path resolve） | **合** |
| B-1.RPM | 是（user 维度精准限流） | 低（自己加 column 也行） | 低 | **合**（前置验证语义不重叠） |
| B-2.monitor | 取决于业务（是否有渠道健康观测工单？） | 中 | 中（4 表 + runner + provider enum 扩展） | **暂缓，等业务确认** |
| B-3.images | 是（用户已用 OpenAI image API） | 低（TK 已自己接了一半） | 低（搭车 Stage A） | **合**（并入 Stage A） |
| B-4.channels view | 否（除非做 ToC 转化） | 低 | 低 | **暂缓** |
| C.misc | 否（小修小补） | N/A | 低 | **不合**（单条 cherry-pick 例外） |

**修订后的最小可执行方案**：

1. **真正要合的只有 1.5 个 stage**：Stage A（auth + payment + 顺手把
 OpenAI images 与 scheduler hot path 一起 resolve）+ Stage B-1（RPM）。
2. Stage B-2 / B-4 / C 默认推迟；按需在后续业务请求时单独切 PR。
3. 这把 review 重量从原本的 "248 commits / 12 PRs" 收敛到
 **2 个 merge PR**，每个 PR 解决一个明确的 TK 用户痛点。

> 这与 §4 的 3-stage 划分**不矛盾**——§4 是按"如果都要合"的最大方案；
> §3.5 是按"按真实价值挑"的最小方案。**优先采用 §3.5 的最小方案**，
> §4 作为"如果业务确认 B-2/B-4 也要"的扩展路径。

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
