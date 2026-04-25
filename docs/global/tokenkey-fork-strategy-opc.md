---
title: TokenKey × sub2api × new-api — Fork 形态与上游融合的 OPC 化方案
status: draft
approved_by: pending
authors: [agent]
created: 2026-04-25
related_prs: []
related_commits: []
related_audit:
  - 2026-04-25 上游漂移测量：origin/main vs upstream/Wei-Shaw/sub2api → TK ahead 91 / behind 257（git fetch upstream main 后 git log/diff 实测）
  - 2026-04-25 上游 3 天合并速率：96 commits（含 12 个 merge PR），与用户报告"3 天 12 PR / 248 commits"一致
  - 2026-04-25 既有 fifth-platform 方案：docs/approved/newapi-as-fifth-platform.md（shipped 2026-04-19）
  - 2026-04-25 既有 traj 抓取方案：backend/internal/observability/qa/* + backend/internal/util/logredact/redact.go
---

# TokenKey × sub2api × new-api — Fork 形态与上游融合的 OPC 化方案

## 0. TL;DR（先读这一段，决定是否往下）

**问题**：tokenkey（本仓库）目前以 `Wei-Shaw/sub2api` 的硬 fork 形式存在，并以 Go module
`replace github.com/QuantumNous/new-api => ../../new-api` 的方式吃 new-api 的 channel/relay 能力。
两个上游都在高速演进——`Wei-Shaw/sub2api` 在最近 3 天内向 `main` 推了 96 commits / 12 个 PR
（用户口径"248 commits / 12 PR"包含 squash 前的开发提交，量级吻合），而 fork 端必须把每一次上游变更
合到 91 个 TK-ahead commits 之上，合并工作量已经成为团队（=1 人 + Cloud Agents）单一最大的时间成本。

**用户硬约束**：

1. tokenkey 以 sub2api 为基础控制面，**统一用户体系**（不分裂 user/group/quota/billing/审计）。
2. **引擎分工**：sub2api 上游负责 OpenAI / Anthropic / Google / Antigravity 四大 OAuth 引擎；
   new-api 上游负责其余所有 OpenAI-兼容渠道（≥40 个 channel_type，含火山/智谱/Moonshot/Doubao 等）。
3. 对外 100% 用 **TokenKey 品牌、视觉、形象**——不暴露 sub2api / new-api 的任何字样、Logo、文案。
4. 所有请求 trajectory（请求体 + 响应体 + SSE chunk + 上下游元信息）**100% 全量脱敏后落盘**，不采样。
5. 整个交付与运维链路必须**面向 OPC**：每条软规则 ↔ 一段机械检查；人工只在高风险审批门禁出现。

**核心结论**（与现 `main` 路线的最大不同，详见 §6）：

| 维度 | 现 `main` 路线 | 本方案 |
|---|---|---|
| sub2api fork 形态 | TK 业务代码直接 inline 进上游文件（`setting_service.go`、`SettingsView.vue` 等）+ companion `*_tk_*.go` | sub2api fork **冻结为"injection point only"**——TK 业务代码以独立 Go module（`tokenkey-extensions`）和 sidecar 表 / 独立 Vue 路由形式注入，**不再触碰上游 ≥200 行的重灾文件** |
| newapi 角色 | "第五平台"（与四大 OAuth 平台并列） | **"引擎接入层"**——newapi = "除四大 OAuth 之外的所有引擎"的统一供给口，调度池语义保留，但用户/管理员视角只看到"渠道分类"，不再有"newapi 平台"这个名字暴露 |
| traj 落盘 | `qa_capture`：默认 enabled、`body_max_bytes=256KiB`（截断采样），脱敏只覆盖 8 个鉴权 key | `traffic_archive`：100% 全量、unbounded body（按 chunk 流式上传 S3）、脱敏由可声明 schema 驱动（`traffic_redact.yaml`），并 enforce "无脱敏 schema 不让上线" |
| 上游合并 | 人工驱动 `merge/upstream-*` PR，靠 `upstream-drift-monitor.yml` 每周提醒 | **自动合并管道**：每日 cloud-agent 自动 dry-run merge，仅在出现真冲突时升级到人工 PR；无冲突时 fast-forward + 自动跑 preflight + 自动 release-tag |
| TK 业务边界 | 与 sub2api 上游纠缠（同一文件内混合）→ 每次 upstream merge 必有冲突 | TK 业务**全部位于 sub2api repo 之外**（独立模块）→ 上游合并的 91% 文件不会被 TK 改动触及 |

---

## 1. 现状盘点（基于 2026-04-25 实测，硬数据 only）

### 1.1 三个仓库的真实关系

```
                ┌─────────────────────────────────────────────┐
                │  TokenKey 用户  /  TokenKey 管理员           │
                │  （只看到 TokenKey 品牌、视觉、形象）         │
                └─────────────────────────────────────────────┘
                                      │
                                      ▼
                 ╔══════════════════════════════════════════╗
                 ║  tokenkey（本仓库 = sub2api 的 hard fork）║
                 ║  - 基础：sub2api upstream 全量代码         ║
                 ║  - 增量：91 个 TK commits（24 个 _tk_ 文件 ║
                 ║    + 设计文档 + 部署 + cloud-agent）       ║
                 ║  - 引用：replace newapi => ../../new-api  ║
                 ╚══════════════════════════════════════════╝
                       │                              │
                       │ git remote upstream          │ go module replace
                       ▼                              ▼
       ┌──────────────────────────┐    ┌──────────────────────────┐
       │ Wei-Shaw/sub2api         │    │ QuantumNous/new-api      │
       │ - 控制面骨架             │    │ - 渠道/Relay 适配器      │
       │ - 4 大 OAuth 引擎        │    │ - 40+ channel_type       │
       │ - User/Group/Quota/Admin │    │ - 上游 SDK / payment SDK │
       │ - 速度：~30 commits/day  │    │ - 速度：中等             │
       └──────────────────────────┘    └──────────────────────────┘
```

### 1.2 上游漂移的硬数据（2026-04-25 实测）

```bash
# git fetch upstream main 之后
$ git log --oneline upstream/main..HEAD | wc -l    # 91   ← TK ahead
$ git log --oneline HEAD..upstream/main | wc -l    # 257  ← TK behind
$ git log --since="3 days ago" --oneline upstream/main | wc -l   # 96 commits
$ git log --since="3 days ago" --merges --oneline upstream/main | wc -l   # 12 merge PRs

# 上游 257 commits 中改动行数最多的 5 个文件（git diff --numstat HEAD..upstream/main）：
#   16070 +/  8911 -   backend/ent/mutation.go             ← Ent 自动生成
#    5641 +/  3094 -   frontend/src/views/admin/SettingsView.vue   ← Admin UI 重灾区
#    1944 +/     0 -   backend/internal/handler/auth_oauth_pending_flow.go
#    1086 +/   201 -   backend/internal/service/setting_service.go ← Setting 重灾区
#     879 +/    73 -   ...
```

**两个观察**：

1. 上游 5641 行改动的 `SettingsView.vue` 是**冲突核爆点**——任何 TK-only 设置只要 inline 进这个文件，每次合并必爆。
2. 上游 16070 行的 `ent/mutation.go` 是**自动生成漂移**——只要任一上游 schema 改动都会撞 TK 已有的 `tk_*` migration。

### 1.3 当前 fork 的"接触面"统计

```bash
$ ls backend/internal/service/ | grep -E "_tk_|tk_" | wc -l    # 24 个 TK companion / test
$ ls backend/internal/integration/newapi/                       # 14 个 newapi bridge 文件
$ rg -l "QuantumNous/new-api" backend/ | wc -l                  # 27 个文件 import newapi
```

但**真正的痛点**不在这 24 个 companion 文件——它们是 TK 自己控制的——而在那些 TK
**改了几行 inline 进上游文件**的注入点（rule §5 "thin injection" 的代价）：

- `setting_service.go`、`admin_service.go` 这些上游热改文件里的"几行 TK 字段/调用"
- `frontend/src/views/admin/SettingsView.vue`、各种 modal 里的"几行 TK 选项"
- `routes/gateway.go` 里挂的 TK helper 函数

每次上游 merge 它们都需要重新人手对齐。

### 1.4 现状方案（fifth-platform）的局限

`docs/approved/newapi-as-fifth-platform.md` 是当前 main 上 newapi 集成的**最佳已落实践**，
解决了"调度池按 group.platform 分桶"+"messages_dispatch 对 newapi 放行"两个具体 P0 缺口。
但它的**适用域是局部**（调度池语义），并不解决以下三件事：

1. **品牌一致性**：admin UI 仍然用 `newapi` / `New API` 字样暴露给运维。
2. **traj 落盘**：`qa_capture` 是采样型 QA，不是合规级"全量"落盘；`logredact` 只有 8 个默认字段。
3. **上游合并工作量**：fifth-platform 做完之后，TK 与上游的接触面**没有减小**，反而因为
   `openai_account_scheduler.go` / `openai_gateway_service.go` 加了 4 处注入点而增大。

本方案在 fifth-platform 的基础上**改变方向**，而不是否定它。

---

## 2. 三方角色重新定位（这是方案的"宪法"）

### 2.1 三层心智模型

```
┌──────────────────────────────────────────────────────────────┐
│ Layer 3: TokenKey Shell（产品层）                             │
│  - 品牌、视觉、形象、定价、用户旅程                              │
│  - cold-start / 充值 / 计费 / 公告 / 看板 / 客服              │
│  - 这一层是 TK **绝对独占**的——上游一行都不该看到这些字眼     │
└──────────────────────────────────────────────────────────────┘
                              ▲
              注入 (hook + sidecar table + 独立 Vue 路由)
                              │
┌──────────────────────────────────────────────────────────────┐
│ Layer 2: sub2api Control Plane（控制面）                      │
│  - User/Group/APIKey/Quota/订阅/Admin 骨架                    │
│  - OAuth 4 大引擎（OpenAI / Anthropic / Google / Antigravity）│
│  - JWT、middleware、route 注册器、Wire DI 骨架                │
│  - 这一层 TokenKey **只读、只 fork、不改业务**                 │
│    （只允许两类改动：bug fix 上游 PR、注入 hook 增加扩展点）   │
└──────────────────────────────────────────────────────────────┘
                              ▲
                  go module replace 引用
                              │
┌──────────────────────────────────────────────────────────────┐
│ Layer 1: new-api Engine Plane（引擎层）                       │
│  - 40+ channel_type 的 relay adaptor                          │
│  - 上游 SDK / dto / 协议适配                                   │
│  - 这一层 TokenKey **只用、不 fork**                          │
│    （需要的功能缺失就 PR 给 QuantumNous，不在 TK 内 hack）    │
└──────────────────────────────────────────────────────────────┘
```

### 2.2 定位重排（与 fifth-platform 的本质区别）

`docs/approved/newapi-as-fifth-platform.md` 把 newapi 摆成"5 个并列平台之一"——这在
**调度层**是对的（一个 group 一个平台、一池一池调度），但在**用户/管理员心智**里它会
泄露 newapi 这个名字（前端有 `OPENAI_COMPAT_PLATFORMS = ['openai', 'newapi']`、admin
UI 有"newapi 平台"选项）。

本方案的重排：

| 既有概念 | 重排后 | 影响层 |
|---|---|---|
| platform = openai | 保留：4 大 OAuth 之一 | 调度 + UI |
| platform = anthropic | 保留：4 大 OAuth 之一 | 调度 + UI |
| platform = gemini | 保留：4 大 OAuth 之一 | 调度 + UI |
| platform = antigravity | 保留：4 大 OAuth 之一 | 调度 + UI |
| **platform = newapi** | **代码层保留**（fifth-platform 调度语义不变）；**UI 层重命名为"OpenAI-Compatible Channel"或具体渠道名（火山/智谱/Moonshot/...）**；**用户/管理员看不到 `newapi` 字样** | 仅 UI / 文案 |

> 这是一个**纯 UI/文案**的改动——`platform=newapi` 在数据库、调度、bridge 层全部保留，
> 只是 admin UI / user UI 把它显示为"OpenAI 兼容渠道（具体渠道由 channel_type 决定）"。
> 调度层的 `IsOpenAICompatPoolMember` / `OpenAICompatPlatforms` 全部不动。

### 2.3 引擎分工的精确边界

| 协议入口 | 调度池来源 | 引擎实现 |
|---|---|---|
| `/v1/messages`（Anthropic 协议） | `group.platform = anthropic` 时直接 → Claude OAuth；`group.platform ∈ {openai, newapi}` 时 → messages_dispatch 转 OpenAI 兼容 | sub2api 上游（Claude OAuth）/ newapi bridge |
| `/v1/chat/completions` | `group.platform ∈ {openai, newapi}`（OpenAI compat pool） | sub2api 上游（OpenAI OAuth）/ newapi bridge |
| `/v1/responses` | 同上 | 同上 |
| `/v1beta/models/*` | `group.platform = gemini` | sub2api 上游（Google OAuth） |
| `/antigravity/v1/*` | `group.platform = antigravity` | sub2api 上游（Antigravity OAuth） |
| `/v1/images/generations` | `group.platform ∈ {openai, newapi}` | newapi bridge（含 Volcengine） |
| `/v1/video/generations` | `group.platform = newapi`（task adaptor） | newapi bridge（VolcEngine / DoubaoVideo） |

**硬规则**：

- TK 不为 4 大 OAuth 平台**新写**任何 relay 代码——bug fix 走上游 PR
- TK 不为 newapi 渠道**新写**任何 relay 代码——bug fix 走 QuantumNous PR
- 唯一的 TK 写代码场景是：**调度策略、计费、限流、品牌 UI、traj 落盘、payment、cold-start**

---

## 3. 五条用户要求 → 设计映射

### 3.1 「用 sub2api 为基础控制面，统一用户体系」

**已满足**：现 main 仓库的 user/group/api_key/quota 表全部是 sub2api 上游 schema。
本方案**禁止**为 TK 业务在 sub2api 的 `User`/`Group`/`APIKey` ent schema 上新增字段——
所有 TK-only 字段一律走 sidecar 表（见 §3.5 表设计纪律）。

**强检查**（preflight 段，新增）：

```bash
# preflight § 11 — sub2api ent schema TK 字段禁入
echo "[preflight] sub2api ent schema TK-only field check"
if rg -nP 'tk_[a-z_]+\s*=' backend/ent/schema/{user,group,api_key,quota}.go 2>/dev/null; then
  echo "FAIL: TK-only field in sub2api shared schema. Use sidecar table tk_user_ext / tk_group_ext / ..."
  exit 1
fi
```

### 3.2 「sub2api 提供 4 大 OAuth 引擎，newapi 提供其他」

**已落地基础**：fifth-platform 调度池语义已 ship。

**本方案补足**：

- 用户 UI / 管理员 UI **不再出现 newapi 字样**（§2.2 文案重排）
- 调度层 `OpenAICompatPlatforms()` 保留 `[openai, newapi]`
- 任何"如果是 newapi 就特殊处理"的代码，必须通过 `IsOpenAICompatPoolMember` 等已有 helper，
  禁止新增第二个谓词（preflight 段 9 已有的 drift check 继续 enforce）

### 3.3 「对外 100% TokenKey 品牌」

**现状**：`README.md` / `assets/` / 部分 admin UI 已经使用 TokenKey 品牌；但 admin UI 的
account/channel 页里仍然能看到 "New API" / "newapi" 字样（来自上游字符串与本仓 UI inline）。

**本方案**（拆为两件 OPC 化的事）：

#### 3.3.1 文案 lint（机械化）

新增 `scripts/check-brand-leak.py`，扫描 `frontend/src/` 下任何用户/管理员可见文件
（`.vue` / `i18n/*.json`），禁止出现以下黑名单词（除非显式 `// brand-allow: <reason>`）：

| 黑名单词 | 允许例外 |
|---|---|
| `New API`、`new-api`、`newapi`（大小写不敏感） | 仅 admin "渠道类型选择" 下拉里的 channel_type catalog（来自上游 ChannelTypeNames）允许，且必须包在 `data-brand-allow="upstream-catalog"` 容器内 |
| `sub2api`、`Sub2API` | 一律禁止 |
| `Wei-Shaw`、`QuantumNous` | 一律禁止 |

接入：`scripts/preflight.sh § 12`（任何分支，pre-commit + CI 都跑）。

#### 3.3.2 主题/Logo/Favicon 强制覆盖

新增 `frontend/src/branding/tokenkey/`，存放 logo / favicon / theme tokens；
`vite.config.ts` 阶段强制 alias 上游品牌资源到 TokenKey 资源；preflight § 12 同时检查
"上游品牌资源未被任何 import 直接引用"。

### 3.4 「100% 全量请求 traj 脱敏落盘」← 最大缺口，§5 单独展开

这是 5 条要求里**与现状差距最大**的一条。当前 `qa_capture` 是采样型 QA 抓取
（`body_max_bytes=256KiB` 截断、`logredact` 只覆盖 8 个鉴权字段），完全不满足"100% 全量脱敏"。

本方案在 §5 设计独立的 `traffic_archive` 子系统（与 `qa_capture` 共生但**职责互不相同**），
确保：

- **覆盖率 100%**：5 个网关入口全部强制挂载，无 sample / 无白名单豁免
- **完整性 100%**：unbounded body，超大 body 走流式分块上传（S3 multipart）
- **脱敏 100%**：基于 `traffic_redact.yaml` 声明式 schema，"未声明的 platform/endpoint 组合不允许上线"
- **可审计**：每条 traj 落盘后产生一个 `archive_receipt`（hash + 大小 + 上传 URL），写到 PG 主表

### 3.5 「面向 OPC」← 是横切要求，不单独成节，散落在 §3-§7

OPC 在本方案里的具体落地（每条都对应一段机械检查）：

| 软规则 | 机械检查 |
|---|---|
| 上游合并工作量必须可控 | `.github/workflows/upstream-auto-merge.yml`（§4）每天 dry-run，无冲突自动 ff，有冲突开 PR |
| TK 业务不污染上游热改文件 | `scripts/check-upstream-touch-budget.py`（§4.3）—— TK PR 改动 sub2api 上游文件超过预算（默认 5 文件 / 50 行）拒绝合入 |
| sub2api ent schema 不被 TK 字段污染 | preflight § 11（§3.1） |
| TK 品牌不泄漏 | preflight § 12（§3.3） |
| traj 落盘 100% 覆盖 | `scripts/check-traffic-archive-coverage.py`（§5.5）—— 5 个入口必须全部挂 `traffic_archive` middleware |
| traj 脱敏 schema 完整 | preflight § 13 —— `traffic_redact.yaml` 必须覆盖所有 (platform × endpoint) 组合 |
| sentinel 反向漂移 | 复用现 `scripts/newapi-sentinels.json`（已有），扩展条目（§4.4） |

---

## 4. OPC 化的上游合并管道（核心杠杆）

这是本方案对"上游合并工作量"问题的**唯一杠杆**。其他改动都是为这条管道服务的。

### 4.1 现状管道

```
人 → 看到 upstream-drift-monitor 周一邮件 → 手动 git fetch upstream → 手动 git merge --no-ff
   → 手动解决冲突 → 手动跑 make test → 手动开 merge/upstream-* PR
   → 等 PR review → 手动选 "Create a merge commit" → 合入
```

实测：每次合并 ~1-2 小时人工。一周一次=每月 4-8 小时。再叠加冲突高峰（sub2api 上游一周
推 ~200 commits 时），月成本超过 12 小时——这是 OPC 体系下**最大的可见浪费**。

### 4.2 目标管道（自动化）

```
cron 每日 03:00 UTC
  ↓
.github/workflows/upstream-auto-merge.yml （Cloud Agent）
  ↓
git fetch upstream main → git merge-tree HEAD upstream/main 检测冲突
  │
  ├─ 无冲突 ───→ 创建 merge/upstream-YYYYMMDD 分支 → git merge --no-ff
  │              → 自动跑 preflight + make test
  │              → 自动开 PR（标签 `auto-merge:clean`，body 含 §5.y 审计 cadence）
  │              → 由 .github/workflows/upstream-merge-pr-shape.yml 校验形状
  │              → 若 CI 全绿 + reviewer 已审过此前 N 次 clean merge：开启 auto-merge label
  │              → 24h 后自动合入（或 reviewer 主动合）
  │
  ├─ 仅 TK companion 文件冲突 ───→ 同上，但标签 `auto-merge:tk-only-conflict`
  │              → reviewer 必须看一眼但不需要写代码，因为 _tk_*.go 是 TK 完全自治的
  │
  └─ 触及上游热改文件冲突 ───→ 标签 `auto-merge:hard-conflict`
                 → 自动 @ assignee + 列出冲突文件清单 + 给出 hint（"可能是 setting_service.go
                   的 inline TK 字段，考虑迁移到 sidecar"）
                 → 人介入解决
```

### 4.3 上游接触面预算（Touch Budget）

新增 `scripts/check-upstream-touch-budget.py`，对**任何非 `merge/upstream-*` PR** 执行：

```bash
# 计算 PR 改动了多少 sub2api upstream 文件 / 行
upstream_files=$(git diff --name-only upstream/main..HEAD | grep -v '_tk_' | grep -v 'docs/' | wc -l)
upstream_lines=$(git diff --shortstat upstream/main..HEAD -- $(<list above>) | parse)

# 默认预算（可在 PR 描述里 `upstream-touch-budget: 10/200` 显式提升）
if [[ $upstream_files -gt 5 || $upstream_lines -gt 50 ]]; then
  echo "FAIL: upstream touch budget exceeded ($upstream_files files / $upstream_lines lines)"
  echo "Move TK logic to companion (_tk_*.go) or sidecar table; refactor approved? add 'upstream-touch-budget: N/M' to PR body"
  exit 1
fi
```

**意图**：用机械门把"TK 业务代码 inline 进上游热改文件"的发生率压到 0；任何超预算的 PR
必须显式说明，让 reviewer 主动评估。

### 4.4 Sentinel 反向漂移扩展

现有 `scripts/newapi-sentinels.json` 已经覆盖 newapi 第五平台载体。本方案新增 sentinel
类别（同一 JSON、同一脚本、同一 preflight 段，零额外脚本）：

| 类别 | 新增 sentinel 条目（示例） | 失败后果 |
|---|---|---|
| traj 落盘 | `service/traffic_archive_service.go::Submit`、`server/middleware/traffic_archive.go::Middleware` | traj 全量落盘失效 |
| 品牌防泄漏 | `frontend/src/branding/tokenkey/index.ts::brand`、`scripts/check-brand-leak.py` 自身存在 | 品牌守卫失效 |
| sub2api hook | `internal/server/routes/gateway.go::tkOpenAICompat*` 已有 + 未来 hook 注入点 | TK 注入入口被静默删除 |
| sidecar schema | `ent/schema/tk_user_ext.go`、`tk_group_ext.go`、`tk_traffic_archive.go` 文件存在 | 用户/组扩展能力失效 |

---

## 5. 全量 traj 脱敏落盘子系统：`traffic_archive`

### 5.1 与现 `qa_capture` 的边界

| 属性 | `qa_capture`（现，保留） | `traffic_archive`（新增） |
|---|---|---|
| 目的 | QA 复盘、调试、用户工单 | **审计 + 合规 + 全量行为留痕** |
| 覆盖率 | 默认 enabled，但允许 disable / 采样 | **强制 100%**，不可 disable，不可采样 |
| Body 大小 | 256 KiB 截断 | **unbounded**，> 1 MiB 走 S3 multipart 流式 |
| 脱敏 | 8 个默认 key + 正则兜底 | **声明式 `traffic_redact.yaml`**：每个 (platform × endpoint) 必须显式声明 redaction schema |
| 存储 | PG `qa_records` + S3 blob（可选 localfs） | **PG `traffic_archive` 表（元信息 + 哈希）+ S3（强制）**，PG 与 S3 失败任意一个都返回 5xx 给客户端 |
| 失败模式 | 软失败（落盘失败不影响请求） | **硬失败可选**：`traffic_archive.fail_mode = "fail_open" \| "fail_closed"`（默认 fail_closed，即落盘失败 = 请求 503） |
| 保留期 | 60 天（可配） | **法定保留期**（默认 365 天，由 `traffic_archive.retention_days` 配置） |

### 5.2 数据通路

```
HTTP Request
  → middleware/traffic_archive.go ── 抢先克隆 request body（streaming Tee）
      │
  → existing handler chain
      │
  → middleware writes response body via tee buffer
      │
  → request 完成 → traffic_archive.Submit(envelope)
      │
  → service/traffic_archive_service.go
      │
      ├─ apply traffic_redact.yaml schema → 产出 redacted_envelope
      │
      ├─ 写 PG `traffic_archive`：request_id, user_id, api_key_id, account_id,
      │    platform, endpoint, status_code, duration_ms,
      │    request_size, response_size, request_sha256, response_sha256,
      │    redaction_schema_version, blob_uri, fail_mode_used
      │
      └─ 写 S3 (multipart)：键 `traffic/<yyyy>/<mm>/<dd>/<request_id>.zst`
           内容 = zstd(JSON{ inbound, outbound, sse_chunks[], headers, ... })
```

### 5.3 脱敏 schema 声明式

`backend/internal/observability/archive/traffic_redact.yaml`（文件 = 单一事实来源）：

```yaml
version: 1
# 每条规则定义"在哪个 (platform, inbound_endpoint) 上、什么字段需要脱敏到什么程度"
rules:
  - platforms: [openai, newapi]
    endpoints: [/v1/chat/completions, /v1/messages, /v1/responses]
    request:
      redact_keys:
        - messages[*].content                   # 用户原文
        - messages[*].tool_calls[*].arguments
      preserve_keys:                            # 显式保留（用于审计指标）
        - model
        - stream
        - tool_choice
    response:
      redact_keys:
        - choices[*].message.content
        - choices[*].message.tool_calls[*].arguments
      preserve_keys:
        - id
        - usage
        - finish_reason
    sse:
      redact_paths:
        - data.choices[*].delta.content
        - data.choices[*].delta.tool_calls[*].function.arguments

  - platforms: [anthropic]
    endpoints: [/v1/messages]
    request:
      redact_keys:
        - messages[*].content
        - system
      preserve_keys: [model, max_tokens, stream]
    response:
      redact_keys:
        - content[*].text
      preserve_keys: [id, usage, stop_reason]
    sse:
      redact_paths:
        - data.delta.text

  # ... gemini / antigravity / images / video 同结构
```

**强检查**（preflight § 13）：

```bash
echo "[preflight] traffic_redact schema coverage check"
python3 scripts/check-traffic-redact-coverage.py \
    --schema backend/internal/observability/archive/traffic_redact.yaml \
    --routes backend/internal/server/routes/gateway.go \
    --route-list scripts/traffic-archive-routes.json \
  || { echo "FAIL: route uncovered by traffic_redact.yaml"; exit 1; }
```

`scripts/traffic-archive-routes.json` 是声明式列表，列出"必须被 traffic_archive 覆盖的入口"——
新增任何 gateway 路由的 PR 必须在同一 commit 内更新此文件，否则 `check-traffic-archive-coverage.py`
会发现 `routes vs schema` 漂移并 fail。

### 5.4 失败模式与 SLA

- 默认 `fail_mode = fail_closed`：落盘失败 → 5xx；这是合规优先级。
- 灰度阶段允许 `fail_mode = fail_open`，但生产 ENV 必须 `fail_closed`，由 `scripts/preflight.sh § 14` 检查
  `deploy/aws/*.env` 中此项不为 `fail_open`。
- 性能预算：traj 落盘的同步部分（compute hash + Submit 到 worker pool）必须 < 5 ms p99；
  worker pool 异步化 S3 上传。
- DLQ：落盘失败的 envelope 落 `/data/traffic_archive_dlq/`，每日 retry job。

### 5.5 覆盖率检查

```bash
echo "[preflight] traffic_archive coverage check"
# 5 个网关入口必须全部挂 traffic_archive middleware
required_routes=(
  "/v1/chat/completions" "/v1/messages" "/v1/responses"
  "/v1beta/models" "/antigravity/v1"
  "/v1/images/generations" "/v1/video/generations"
)
for route in "${required_routes[@]}"; do
  rg -F "$route" backend/internal/server/routes/gateway.go | rg -q 'trafficArchive' \
    || { echo "FAIL: $route missing trafficArchive middleware"; exit 1; }
done
```

### 5.6 与 `logredact` 的关系

`logredact`（现）是**正则兜底层**——它处理"日志、错误信息这种结构未知的字符串"。
`traffic_archive` 是**协议感知层**——它知道 OpenAI / Anthropic / Gemini 协议结构，按结构脱敏。

两者**不替代彼此**：traffic_archive 负责协议 body，logredact 负责非结构化日志。
preflight § 13 只校验 traffic_archive 的 schema 覆盖率；logredact 的 8 字段保持不变。

---

## 6. 与现 `main` 仓库方案的逐项对照

> 本节是用户特别要求的「指出和现有 main 仓库方案的区别」。
> 对照基准：`docs/approved/newapi-as-fifth-platform.md` (shipped) +
> `docs/approved/admin-ui-newapi-platform-end-to-end.md` (shipped) +
> `CLAUDE.md` 当前规则集。

### 6.1 角色定位

| 维度 | 现方案 | 本方案 |
|---|---|---|
| newapi 在用户/管理员心智里的位置 | "第五平台"（与 4 大 OAuth 并列出现在 platform 选择器） | 渠道接入层（用户不见 newapi 字样；管理员视角是"OpenAI 兼容渠道"+"channel_type 子分类"） |
| TK 业务代码与上游的物理边界 | 同 repo、同包，靠 `_tk_*.go` 文件命名约定隔离 | 同 repo、但 TK 业务代码集中到 `internal/tkext/`（独立子目录）+ 未来可剥离为独立 Go module；上游文件几乎不动 |
| sub2api 在 TK 视角里的角色 | 上游骨架 + 业务 partner（双方共同贡献 user/group/billing/UI 等） | **只有控制面骨架**（user/group/auth/UI 框架）；业务能力（计费、cold-start、payment）由 TK 独占 |

### 6.2 上游融合策略

| 维度 | 现方案 | 本方案 |
|---|---|---|
| 上游合并触发 | 人工，靠 weekly drift monitor 提醒 | 每日 cron + cloud-agent 自动 dry-run |
| 上游合并 PR 形态 | 人工开 `merge/upstream-YYYYMMDD` | clean merge 自动开 PR + 自动跑 CI；冲突才升级人工 |
| 上游接触面控制 | 软规则（CLAUDE.md §5 "thin injection point"） | 硬规则（`check-upstream-touch-budget.py` 默认 5 文件 / 50 行预算） |
| TK 字段进入上游 ent schema | 软规则（"prefer companion file"） | 硬规则（preflight § 11 拒绝） |

### 6.3 traj 落盘

| 维度 | 现方案 (`qa_capture`) | 本方案 (`traffic_archive` + 保留 qa_capture) |
|---|---|---|
| 覆盖率 | 默认 enabled、可 disable | 强制 100%、不可 disable |
| Body 大小 | 256 KiB 截断 | unbounded（流式 multipart） |
| 脱敏方式 | 默认 8 个 key + 正则 + 可注入 extra keys | 声明式 yaml schema、按 (platform × endpoint) 列表必须覆盖 |
| 失败模式 | 软失败 | fail_closed（生产强制） |
| 保留期 | 60 天 | 365 天（合规口径，可配） |
| 元数据写入 | `qa_records` 表 | 新表 `traffic_archive`（元信息）+ S3（blob） |
| Preflight 检查 | 无 | § 13（schema 覆盖）+ § 14（fail_mode） |

### 6.4 品牌一致性

| 维度 | 现方案 | 本方案 |
|---|---|---|
| 品牌词检查 | 无机械门，靠 reviewer 肉眼 | `scripts/check-brand-leak.py` + preflight § 12 |
| Logo / Favicon | TokenKey 资源已有但与上游资源混在 `frontend/src/assets/` | 集中到 `frontend/src/branding/tokenkey/` + vite alias 强制覆盖 |
| Newapi 字样在 admin UI | 直接显示 `newapi` 平台 | 显示为 "OpenAI Compatible Channels"，channel_type 子分类显示具体名（火山/智谱/...） |

### 6.5 OPC 化进度（每条软规则的硬检查）

| 软规则 | 现方案硬检查 | 本方案硬检查 |
|---|---|---|
| newapi 调度池漂移 | `preflight § 9`（已有，drift-forward） | 保留 |
| sentinel 反向漂移 | `scripts/newapi-sentinels.json`（已有，9 entries） | 扩展到 ~14 entries（含 traj、品牌、sidecar） |
| 上游合并 PR 形状 | `.github/workflows/upstream-merge-pr-shape.yml`（已有） | 保留，新增 `auto-merge:*` 标签 + 自动 ff 路径 |
| 接近度 / 上游接触面 | 无 | **新增** `check-upstream-touch-budget.py` |
| sub2api ent schema TK 字段 | 无 | **新增** preflight § 11 |
| 品牌词泄漏 | 无 | **新增** preflight § 12 + `check-brand-leak.py` |
| traj 全量脱敏覆盖 | 无 | **新增** preflight § 13 + § 14 |
| 上游自动合并 | 无 | **新增** `.github/workflows/upstream-auto-merge.yml` |

### 6.6 哪些不变（重要）

- **fifth-platform 调度池语义全部保留**：`IsOpenAICompatPoolMember` / `OpenAICompatPlatforms` /
  `isOpenAICompatPlatformGroup` / `messages_dispatch_model_config` 行为完全不动。
- **bridge dispatch 路径不变**：`ShouldDispatchToNewAPIBridge` + `bridge.Dispatch*` 不动。
- **ent schema 不动**：本方案 0 张新表落到 sub2api 上游 ent；新表全部走 sidecar
  （`ent/schema/tk_*` 文件，与上游 schema 解耦）。
- **路由规则不动**：5 个网关入口 + 路由分发逻辑保持现状，只新增 `trafficArchive` middleware。
- **Wire DI 骨架不动**：traffic_archive 通过现有 `wire.go` 注册，不改 wire 拓扑。
- **既有 cold-start / signup-bonus / trial-key 等业务**：保留在现位置，本方案只阻止"未来"
  的同类业务扩散到上游热改文件。

---

## 7. 实施路线（按技术依赖排序，不预估工时）

> Cloud Agent 不预估天数；本节按"必须先做什么、做完才能做什么"组织。

### 7.1 P0 / 阻塞项（必须先做）

1. **traj 全量脱敏（§5）**——这是唯一一条目前完全未满足的硬约束。包括：
   - 新增 `traffic_archive` ent schema（sidecar 表，与上游 schema 解耦）+ migration
   - 新增 `internal/observability/archive/` 包（service + middleware + redact engine）
   - 新增 `traffic_redact.yaml` 与 `scripts/check-traffic-redact-coverage.py`
   - 5 个网关入口挂载 middleware
   - `scripts/preflight.sh § 13 / § 14` 接入
   - 集成测试（testcontainer，覆盖 5 个入口 × 4 个协议 × redaction round-trip）
   - `docs/approved/traffic-archive-fullcover.md`（本设计的子设计文档）

2. **上游接触面预算（§4.3）**——这是控制后续工作量的元约束：
   - 新增 `scripts/check-upstream-touch-budget.py`
   - 接入 `scripts/preflight.sh § 15`
   - 在 CLAUDE.md §5.x 之后新增 §5.z "Upstream touch budget" 段，描述例外申请方式

### 7.2 P1 / 与 P0 解耦但依赖产品确认

3. **品牌泄漏检查（§3.3）**：
   - 新增 `scripts/check-brand-leak.py`
   - `frontend/src/branding/tokenkey/` 集中化
   - vite alias 配置
   - 接入 `scripts/preflight.sh § 12`
   - **依赖**：产品给出"哪些场景允许显示 channel_type 原始名"的清单（黑名单例外）

4. **Admin UI 文案重排（§2.2）**：
   - 把 "newapi" 改为 "OpenAI Compatible Channels"
   - channel_type 选择器使用具体厂商名（火山/智谱/Moonshot/...）
   - **依赖**：3 完成（避免新文案再次混入禁词）

### 7.3 P2 / 价值最大但风险最高（最后做）

5. **上游自动合并管道（§4.2）**：
   - 新增 `.github/workflows/upstream-auto-merge.yml`
   - 与现 `upstream-merge-pr-shape.yml` 联动
   - 灰度：第一周只跑 dry-run + 通知，不真正合
   - **依赖**：1 + 2 已 ship——否则 auto-merge 把"未脱敏的 traj 路径上线了"

6. **sub2api ent schema TK 字段守卫（§3.1）**：
   - 当前代码已经基本符合（24 个 _tk_ 文件都不动 ent schema）
   - 新增 preflight § 11 主要是防御未来回归
   - **依赖**：无

### 7.4 不在本方案范围（明确"不做"）

| 不做 | 原因 |
|---|---|
| 把 sub2api fork 改成"thin shell + extension module" | 工程量太大，且收益主要是"代码物理隔离"，靠 §4.3 touch budget 用机械门已经能拿到 80% 收益 |
| 把 newapi `replace` 改成正式 module 版本依赖 | new-api 不发版本 tag；现 commit-pinning（`.new-api-ref`）已经能保证 reproducibility |
| 把 4 大 OAuth 引擎也迁到 newapi | 违反"sub2api 提供 4 大 OAuth、newapi 提供其他"的用户硬约束 |
| 自建 channel_type catalog | 复用上游 `ChannelTypeNames`（newapi 已有），自建反而引入维护负担 |
| 把 admin UI 重写为独立 SPA | 现 admin UI 与 sub2api 上游 SettingsView.vue 强耦合；重写工程量超过本方案任何一条 P0/P1 |

---

## 8. 风险与回滚

### 8.1 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| traffic_archive `fail_closed` 在 PG/S3 抖动时把 5xx 放大 | 中 | 灰度阶段 `fail_open`；prod 切换前压测；DLQ + 异步重试兜底 |
| `check-upstream-touch-budget` 误伤合理的 sub2api bug fix PR | 低 | PR 描述支持 `upstream-touch-budget: N/M` 显式提升预算；reviewer 评估 |
| 上游自动合并把"语义破坏但编译通过"的变更合入 | 中 | auto-merge 只对 `auto-merge:clean` 标签生效；reviewer 必须看一眼；24h 自动合并窗口期可中断 |
| 品牌词检查误伤上游 channel_type catalog | 中 | 显式 `data-brand-allow="upstream-catalog"` 例外机制 |
| Schema 漂移：上游为 newapi channel 新增了 TK 没声明的 endpoint | 高 | preflight § 13 立即 fail；强迫开发者补 schema |

### 8.2 回滚

每个 P0/P1/P2 项都是独立 PR、独立 feature flag：

- traffic_archive：`traffic_archive.enabled = false` 关闭，回退到 `qa_capture` 单跑
- touch budget：preflight 段 15 删除即回滚
- 品牌检查：preflight 段 12 删除即回滚
- 自动合并：禁用 workflow 即回滚

---

## 9. 验收清单（合并门禁）

### 9.1 P0 验收

- [ ] `traffic_archive` 表落地 + migration 通过 + ent generate 完成
- [ ] 5 个网关入口的 integration test（testcontainer + Postgres + minio）证明 100% 落盘
- [ ] `traffic_redact.yaml` 覆盖 5 个 platform × 7 个 endpoint 矩阵 100%
- [ ] `scripts/check-traffic-redact-coverage.py` 行为 negative test：删除一条规则 → exit 1
- [ ] `scripts/preflight.sh § 13 § 14` 接入 + 行为验证可 fail
- [ ] `fail_closed` 模式下 PG 故障时返回 503 的 e2e 测试
- [ ] `check-upstream-touch-budget.py` 在本仓 mock PR 上的行为验证

### 9.2 P1 验收

- [ ] `scripts/check-brand-leak.py` 跑过当前 frontend 全树 + negative test（注入 "New API" → exit 1）
- [ ] vite alias 验证：上游 logo/favicon 不被任何 import 引用
- [ ] admin UI screenshot 评审：用户/管理员视角全程无 newapi / sub2api 字样

### 9.3 P2 验收

- [ ] `upstream-auto-merge.yml` 灰度 7 天 / dry-run-only
- [ ] 一次完整 clean merge auto-flow 跑通（PR 自动开 + CI 自动跑 + label `auto-merge:clean`）
- [ ] 一次 hard-conflict 升级流程跑通（label `auto-merge:hard-conflict` + 人工接管）

### 9.4 回归（5 条用户要求）

- [ ] 用户体系统一：`User`/`Group`/`APIKey` 上无任何 TK-only 字段（preflight § 11 通过）
- [ ] 引擎分工：fifth-platform 调度池语义不变（`go test -tags=unit ./internal/service/... -run 'TestUS00[89]_|TestUS01[1-5]_'` 全绿）
- [ ] 品牌一致性：`scripts/check-brand-leak.py` 通过
- [ ] 全量脱敏落盘：覆盖率 100% + schema 覆盖率 100%（preflight § 13 通过）
- [ ] OPC 化：本设计涉及的 6 条新软规则全部有对应 preflight 段

---

## 10. 与既有 approved 设计的关系

| 既有设计 | 本方案对它的态度 |
|---|---|
| `docs/approved/newapi-as-fifth-platform.md` | **保留**调度池语义；**修订**用户/管理员可见性（newapi 字样不再出现） |
| `docs/approved/admin-ui-newapi-platform-end-to-end.md` | **保留**端到端 UI 流程；**修订**文案（§2.2、§3.3） |
| `docs/approved/sticky-routing.md` | **不动** |
| `docs/approved/user-cold-start.md` | **不动**；后续此类业务一律按 §3.1 走 sidecar |
| `docs/approved/deploy-stage0-workflow.md` | **不动**；§7.3 的自动合并管道是它的补充，不替代 |
| `docs/approved/openai-codex-as-claude-thinking-continuity.md` | **不动** |
| `docs/approved/newapi-followup-bugs-and-forwarding-fields.md` | **不动**；列入 P2 阶段同步关注 |

任何与本方案冲突的既有 approved 设计，需在本方案 approval 后单独提 PR 修订其 frontmatter
（按 `docs/approved/` lifecycle 规则）。

---

## 11. OPC 哲学贯穿（结尾自检）

| 哲学要点（dev-rules `digital-clone-research.md §一`） | 本方案如何兑现 |
|---|---|
| 杠杆最大化 | §4 自动合并 + §4.3 touch budget = 把"上游合并"这条最贵的工作流自动化 |
| 流程极简 | 不为合并新增任何手工 checklist；新增的全是脚本 |
| 自动化优先 | §4 / §5.5 / §3.1 / §3.3 全部以 preflight 段 + workflow 形式落地，**禁止**任何"靠自觉"的口头约定 |
| 深度 > 广度 | 不做"再多接一个上游/再多 fork 一个 repo"；把 sub2api + newapi 两条线吃透 |
| 反脆弱 | 每条规则代码化（preflight）+ 版本化（git）；任意一台机器丢失都不影响系统 |
| 聚焦（Jobs） | §7.4 明确"不做"清单；不为 future-proof 抽象引入第六平台 |

> 如果本方案的某一段在落地后**没能换成一段可执行的脚本/工作流**——
> 就意味着它退化成了"靠自觉"。该段必须删除或重写为机械检查，否则不是 OPC，是文档艺术。
