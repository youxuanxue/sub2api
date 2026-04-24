---
title: TokenKey 双栈分部署提议 vs 单一融合 fork — 多维度分析与替代方案
status: draft
approved_by: pending
authors: [agent]
created: 2026-04-24
related_docs:
  - CLAUDE.md §5 (Upstream Isolation)
  - docs/approved/newapi-as-fifth-platform.md
  - docs/approved/deploy-stage0-workflow.md
  - docs/users/downstream-tools-and-user-segments.md
related_prs: []
scope: "针对『把 sub2api 和 new-api 拆成两套独立部署，对外用 tokenkey 统一品牌』提议的合理性评估，以及给出更符合 OPC 的替代方案"
---

# TokenKey 双栈分部署提议 vs 单一融合 fork — 多维度分析

## 0. TL;DR（先给结论）

**对原提议的判断：核心拓扑（拆成 sub2api + new-api 两套独立部署）不应采纳。**

提议命中了一个真问题（上游漂移压力大、需要服务不同地理区域的用户群），但提议
的解法把"代码融合的复杂度"换成了"基础设施 × 4 + 用户体系打通 + 跨境数据同步"
的更大复杂度。从 OPC（1 人 + N Agent）和 Jobs 聚焦视角，这是把痛苦从上游
合并环节转移到运维 + 账户中心环节，**总成本不降反升**。

**替代方案（推荐）：保持单一融合 fork，把痛点分层解决。**

| 用户表述的痛点 | 提议的解法 | 推荐解法 |
|---|---|---|
| 上游漂移压力大 | 拆产品停止融合 | **分批 cadence 化 upstream merge**（已有 §5.y.1 工具链；继续做） |
| 100% 全量脱敏落盘 | （未直接解决） | **在 gateway forward 路径插入统一 audit/redact middleware**（与架构正交） |
| 地理分区 + 不同用户群 | 拆产品 + 4 域名 + 跨境同步 | **同一份代码多 region 部署**（IaC 复用 `cicd-oidc.yaml` + stage0 模板） |
| 1 个用户跨产品共享额度 | 拆 + 跨服务账户同步 | **本来就在一套用户体系内**（融合架构的副产品） |
| 统一品牌/视觉 | 给两套 admin UI 套同一个 logo | **本来就是一份前端**（融合架构的副产品） |

**OPC 评分**：

- 原提议：**反 OPC**（4 套独立栈 = 4 倍 PR/CI/Agent/secret/合规面）
- 推荐方案：**强 OPC**（1 份代码 + N region 部署，IaC 横向复制）

下文是支撑这个结论的事实链与论证。如果你只想看推荐方案的执行轮廓，跳到 §5。

---

## 1. 现状事实盘点（基于 2026-04-24 main 分支代码）

### 1.1 TokenKey 不是"sub2api 旁边放 new-api"，是"sub2api fork 内嵌 new-api"

事实链（来源：`CLAUDE.md` § Project Overview / § Hard Rules.4 / § Hard Rules.5；
`docs/approved/newapi-as-fifth-platform.md`）：

| 维度 | 当前实现 |
|---|---|
| 代码所有权 | TokenKey 是 `Wei-Shaw/sub2api` 的 fork（一份 git 仓库）；`QuantumNous/new-api` 通过 `go.mod replace` + sibling clone 引入 |
| 上游 pin | `.new-api-ref` 钉死一个 SHA（`f995a868`），CI、Docker build、本地 dev 用同一 SHA |
| 调度融合层 | `newapi` 已是 first-class 第五平台（`PlatformNewAPI` 常量 + `IsOpenAICompatPoolMember(groupPlatform)` 调度池语义 + bridge dispatch），不是侧栏功能 |
| 用户/账户/计费 | 单一 PostgreSQL + Ent ORM，单一 user table、token table、quota table；new-api 的 GORM 数据层**不被引用**（CLAUDE.md §5「NEVER call GORM DB operations from New API code」） |
| 前端 | 单一 Vue 3 + pnpm 工程，`frontend/src/`，admin UI 与 gateway 用户 UI 共用一份 |
| 部署单元 | 单 Docker 镜像（`Dockerfile`）含 backend + 嵌入式 frontend；当前 1 prod + 1 test，AWS Graviton 单 region |
| 运维自动化 | `.github/workflows/deploy-stage0.yml`（cloud-agent 触发 SSM 升级）、`cicd-oidc.yaml`（IAM trust） |
| 上游隔离纪律 | `CLAUDE.md` §5/§5.x/§5.y/§5.y.1 全套，含 `scripts/check-upstream-drift.sh`、`upstream-merge-pr-shape.yml`、`scripts/newapi-sentinels.json` |

**关键观察 1**：用户在提议里把 TokenKey 描述成"集成 sub2api 和 new-api 两个
开源项目优势"——这个描述把**融合的强度**讲弱了。当前的 `newapi-as-fifth-platform`
不是"调用 new-api 的能力"，而是"new-api 的 channel 概念被解构成 TokenKey
account/group 的一个平台维度，与 OpenAI/Anthropic/Gemini/Antigravity 共享同
一个调度器、同一个计费链路、同一个 sticky session、同一个 messages_dispatch
配置链"。

**关键观察 2**：TokenKey 已经付出过把这件事做对的代价——见
`docs/approved/newapi-as-fifth-platform.md` 的 12 节、8 篇 user story、34 个
单元测试、3 道 preflight 段、1 个 sentinel registry。这是不可回收的存量
设计资产。

### 1.2 上游漂移压力的真实数据

现场测得（branch: `cursor/tokenkey-dual-stack-proposal-analysis-0326`，
upstream remote `Wei-Shaw/sub2api`）：

```
TK ahead of upstream/main    : 91 commits
TK behind upstream/main      : 257 commits
new-api 自 pin SHA 后的进展  : ~50 commits（pin = f995a868）
```

用户说"sub2api 3 天合并 12 个 PR、248 个 commits"——这个数量级在数据面上
被验证（差不多一个月就攒到 257）。所以**上游漂移压力是真实的痛点**，不是
情绪化抱怨。

但需要拆开看：

| 漂移分量 | 是否真痛 | 可控手段 |
|---|---|---|
| sub2api upstream 的 257 commits 多数是什么 | 主要是 codex / openai 调试细节、payment / monitor、UI 调整；与 TokenKey 五平台调度核心几乎不冲突 | `scripts/check-upstream-drift.sh` 监控 + 双周 cadence 合 |
| new-api 上游 50 commits | 多数是 channel adapter 修复、计费修复 | `.new-api-ref` bump 是机械步骤，本身不痛；痛在桥接层兼容 |
| **真痛的部分** | 当 sub2api / new-api 同时改了 TokenKey 的注入点（如 `openai_account_scheduler.go`、`bridge/dispatch.go`）| sentinel + companion 文件已经把热点收敛到可枚举范围 |

**结论**：上游漂移痛是真的，但它的**根因不是融合架构**，而是
"积累式合并 + 手工合并"。已有的 §5.y.1 机械门槛已经把"合并安全"做完了，
缺的是 **cadence**——把"积累式手工合并"换成"双周自动 cadence"。

### 1.3 用户群与下游工具事实

来源：`docs/users/downstream-tools-and-user-segments.md`（2026-04-14 调研）。

- 中国用户主力工具：Cherry Studio、NextChat、Cursor、Cline、SillyTavern
- 这些工具 99% 通过 OpenAI 兼容协议接入；少量通过 Anthropic 兼容协议
- 主流场景：编程（>50% token）、聊天、角色扮演、翻译

提议里"中国用户走 OAI/Anthropic/Google、海外用户走 kimi/ark/deepseek"这
个用户分群假设，与调研数据**部分对不上**：

- ✅ 中国用户主力消费 OAI/Anthropic/Google 模型（拼车买订阅再分发）—— 与
  sub2api 的核心定位完全契合
- ❌ 海外用户主力消费 kimi/ark/deepseek？—— 调研里**找不到证据**。海外
  开发者社区（OpenRouter 数据）显示编程场景的主力模型是 Claude Sonnet /
  Gemini / GPT-5；DeepSeek 在海外有声量，但远不到"主力"。Kimi、豆包（ark）
  在海外几乎没有成规模需求

**结论**：用户分群假设的左半部分（中国 → OAI/Anthropic）成立；右半部分
（海外 → 国产模型）作为产品规划的前提**证据不足**，不应据此做架构决定。

---

## 2. 对原提议的逐条评估

### 2.1 提议要素拆解

| # | 提议要素 | 性质 |
|---|---|---|
| P1 | sub2api 部署在 AWS 美国/新加坡/日本，给中国用户用 OAI/Anthropic/Google | 拓扑分区 |
| P2 | new-api 部署在 AWS 中国/亚太，给海外用户用 kimi/ark/deepseek | 拓扑分区 |
| P3 | 对外都用 TokenKey 统一品牌、统一视觉、统一形象 | 品牌 |
| P4 | 所有请求 traj 100% 全量脱敏落盘 | 审计/合规 |
| P5 | 同一套用户体系（充值、额度共享） | 账户 |
| P6 | 面向 OPC | 元约束 |

P1 + P2 共同构成"拆双栈"决策；P3/P4/P5 是横切要求；P6 是元约束。

### 2.2 P1 + P2（拆双栈）评估

#### 2.2.1 优点（如果做）

| 优点 | 说明 | 真实价值 |
|---|---|---|
| 上游合并冲突隔离 | sub2api 的合并不会牵动 new-api 的合并 | 中等。但当前的合并冲突主要发生在**桥接层**（`integration/newapi/`、`bridge/`），不是 sub2api ↔ new-api 互相干扰 |
| 故障域隔离 | new-api 整个挂了不影响 sub2api | 低。当前融合代码里两边在不同 endpoint，故障已经天然隔离；新拓扑反而引入了"两个独立服务的网络互联"故障域 |
| 部署/扩缩独立 | 可以单独扩 new-api | 低。当前 stage0 单实例足够；扩容是未来的伪命题 |

#### 2.2.2 致命问题

**Z1. 推翻 `newapi-as-fifth-platform` 的核心价值**

当前融合架构的核心卖点是：**同一个 group 可以同时混用 OAuth 订阅账号和
new-api channel 账号**，**同一个 API Key 可以路由到 Claude OAuth 也可以
路由到 Kimi**，**一站式 K**（`messages_dispatch_model_config`）能把
Anthropic 协议的请求落到 OpenAI 兼容上游。

如果拆成两套独立部署：

- 用户必须在 `cn.tokenkey.example` 申请一份 API Key、在 `global.tokenkey.example`
  另申请一份 API Key
- 同一个工具（Cursor、Claude Code）要配两个 base_url 切换
- 一站式 K 这种跨平台路由直接不存在了
- 所有"统一计费、统一限流、统一并发控制"都要在两个独立服务间用 RPC 实现

这是把已经付出过昂贵代价做对的差异化能力**主动废弃**。

**Z2. P5（用户体系共享）在两个独立服务里几乎做不对**

要做到"1 个用户两边都能用、充值和额度共享"，可选方案：

| 方案 | 实现 | 致命问题 |
|---|---|---|
| (a) 共享 PostgreSQL 库 | 两个 region 直连同一个 PG | 跨境延迟 100~300ms，每次额度扣减都跨境，性能崩溃；数据出境合规风险 |
| (b) 中心账户服务 + RPC | 抽出第三个服务做账户中心，sub2api 和 new-api 都改造成"无状态前端 + 中心账户后端" | 等于把两边都重写成对中心的客户端，那为什么不直接维护一份代码？而且每次请求要多一跳 RPC，p99 上升 |
| (c) 双写 + 同步 | 两边各有 user 表，通过消息队列同步 | 计费天然不一致；同一个 token 在两边可能被双重扣额度或双重透支；做对的代价 = 一个完整的分布式账户系统 |

**而且 sub2api 的用户/账户 schema（Ent）和 new-api 的用户/账户 schema
（GORM）完全不同**——字段语义不一致、quota 单位不一致、token 命名空间
不一致。打通需要做一个**第三系统**（账户中心 + schema mapper），这本身的
工程量就大于"维护现有融合 fork"。

**Z3. P1 的"中国用户访问美国/新加坡/日本节点"前提的现实问题**

中国用户通过公网访问海外节点：

- 访问 AWS us-east：从中国大陆走的国际出口存在不可控的丢包与限速
- AWS ap-singapore / ap-northeast-1：相对好但仍受国际带宽影响
- 解决方案要么走 CN2 GIA 私网线（成本高，不像 OPC）、要么 Cloudflare 中
  国合作版（备案要求）、要么用户自带代理（不可控）

这是 sub2api 类产品**已经在面对的问题**——拆栈不解决它，反而加重：
拆完之后中国用户访问海外节点的网络问题依然存在，但又新增了"在中国境内
为海外用户部署 new-api"的网络对称难题（合规更复杂）。

**Z4. P2 的"海外用户用 kimi/ark/deepseek"用户分群假设证据不足**

见 §1.3。海外开发者主力消费的是 Claude / Gemini / GPT；DeepSeek 在海外有
声量但远谈不上主力；Kimi、豆包在海外几乎无成规模需求。

如果"海外栈"的真实用户量很小，就**不值得**为它单独维护一套基础设施。
这是典型的 Jobs "say no" 时刻。

**Z5. 运维成本爆炸（直接反 OPC，P6 自相矛盾）**

| 维度 | 当前 | 拆双栈后 |
|---|---|---|
| Docker 镜像 | 1 个 | 2 个（不同 build chain） |
| 域名/证书 | 2 个（prod + test） | 4~8 个（每栈各 prod/test，国内外可能再拆） |
| PostgreSQL 实例 | 1 个 prod + 1 个 test | 2~4 个（除非走方案 a 共享，那有 Z2） |
| Redis 实例 | 1 个 prod + 1 个 test | 2~4 个 |
| GitHub workflows | `release.yml` `backend-ci.yml` `deploy-stage0.yml` 等一套 | 2 套，且互相需要协调 |
| Secret 管理 | `cloud-agent.env` 一份 | 至少 2 份 |
| 跨境数据合规 | 不涉及 | 涉及 PIPL 个人信息出境评估、CSL 数据本地化 |
| Cloud Agent 工作流 | 1 个仓库的 PR/issue/CI loop | 2 个仓库各跑一份；用户分群、bug clustering、log dump 都要 ×2 |

OPC 的核心是 "1 人 + N Agent"，杠杆点是**所有自动化复用同一份代码/同一
份配置**。拆栈直接打掉这个杠杆。

#### 2.2.3 P1 + P2 评估结论

**不应采纳**。提议命中的优点（隔离合并冲突、故障域隔离）已经被现有融合
架构 + §5 纪律基本覆盖；提议引入的代价（Z1~Z5）远大于解决的问题。

### 2.3 P3（统一品牌/视觉/形象）评估

如果走拆栈路径，P3 的成本是被 underestimate 的：

- new-api 的 admin UI 是 React + Semi Design；sub2api 的 admin UI 是 Vue 3
  + TailwindCSS。给两边都套 TokenKey logo 是表层装修；用户登录后看到的
  导航、表单、操作流程完全是两套异构 UI
- 国际化、错误提示、表单字段命名两边不一致
- 用户体感的"统一品牌"≠ 给两个 admin 都换 logo

如果走推荐方案（单融合代码库），**P3 是已经达成的副产品**——只有一份
前端、一份 i18n、一份组件库，自然统一。

### 2.4 P4（100% 全量脱敏落盘）评估

**与拆栈与否完全正交。** 这是 gateway forward 路径上的一个 middleware
设计问题：

- 当前融合架构里：在 `internal/service/openai_gateway_service.go` 等转
  发链路插入 `audit_redact` middleware，对 request body / response stream
  做脱敏后异步落盘（S3 / 本地卷 + 定期归档）
- 脱敏字段：API key 头、请求 body 的 `messages[].content` 中的 PII
  pattern（手机号、邮箱、身份证、银行卡）、user-supplied tools schema 等
- 落盘介质：本地 NDJSON + `logrotate` → S3 lifecycle，或直接 Kinesis
  Firehose

无论走拆栈还是单融合，这个能力都需要单独做一篇 design + 实现。**它的存
在不构成拆栈的理由。** 单融合代码库实现一次，所有 region 自动一致；拆
栈的话两边各做一次，行为容易漂移（这恰恰违反 P4 的"100%"要求）。

### 2.5 P5（用户体系共享）评估

见 Z2。**这是反对拆栈的最强论证之一**：

- 单融合代码库：用户体系共享是**默认状态**，不需要做任何额外工作
- 拆栈：必须做一个分布式账户系统，工程量 ≥ 维护现有融合 fork

P5 实际上证伪了 P1 + P2。

### 2.6 P6（OPC 友好）评估

OPC 的两个核心约束：

1. **杠杆最大化**：所有自动化复用同一份产物
2. **流程极简**：每个流程步骤必须挣得自己的位置

拆双栈的所有引入物（4 个域名、4 套 secret、2 套 CI、跨境同步、账户中心）
**没有一项**能在 OPC 框架下证明自己挣到了位置。原提议把"P6 面向 OPC"
作为正面诉求列出，但 P1 + P2 的实质是反 OPC。

### 2.7 提议的整体定性

| 维度 | 评分 |
|---|---|
| 命中真实痛点 | ✅ 部分（上游漂移压力是真的；100% 审计是真需求；多区域接入是真需求） |
| 解法与痛点匹配度 | ❌ 解法在错误的层级——架构层级回应的是基础设施层级和接入层级的问题 |
| 推翻已沉淀资产的代价 | ❌ 极高（`newapi-as-fifth-platform` 的 8 篇 user story、34 个测试、整套 §5 纪律） |
| 引入新复杂度 | ❌ 极高（账户中心、跨境同步、双 CI、双 secret、双合规面） |
| 与 OPC 一致 | ❌ 反向 |
| 与 Jobs 聚焦一致 | ❌ 反向（多做了一倍的产品而不是做深一个） |

**整体判断：建议推翻提议中"拆双栈"部分；保留并放大其中"多区域接入 +
统一审计"的合理诉求。**

---

## 3. 推荐替代方案：单一融合 fork + 多 region 同栈部署 + 强 OPC 闭环

### 3.1 核心命题

> **不是拆产品，是把同一个产品复制到多个 region。**

类比：Netflix、Cloudflare、Stripe 都不是"在不同地区做不同的产品"，而是
"同一份代码部署到所有 region，数据按合规边界分库或集中"。

### 3.2 分层设计

```
                  ┌───────────────────────────────────────────────┐
                  │      用户接入层（Anycast / DNS 智能解析）     │
                  └───────────────────────────────────────────────┘
                                     │
              ┌──────────────────────┼──────────────────────┐
              │                      │                      │
        region: us-east        region: ap-sea          region: cn-*
        (TokenKey GW)          (TokenKey GW)           (TokenKey GW，可选)
              │                      │                      │
              └──────────────────────┼──────────────────────┘
                                     │
                          ┌──────────┴──────────┐
                          │   中心账户/计费库    │
                          │  (PostgreSQL Primary │
                          │   + read replicas)   │
                          └─────────────────────┘
```

- **接入层**：DNS 智能解析或 Anycast，让用户被路由到最近的 region
- **gateway 层**：每个 region 跑同一份 TokenKey 镜像（同一个 Docker tag）
- **数据层**：单 PostgreSQL 主库 + 各 region 只读 replica；写操作通过主
  库（额度扣减用 Redis 原子操作 + 异步落 PG 模式，见现有
  `sticky-routing.md`）
- **跨 region 调度状态**：Redis（每 region 一个，sticky session 本来就
  是 region-local 的）

### 3.3 该方案如何回应原提议的每条要素

| 提议要素 | 当前融合架构现状 | 方案 X 增量 |
|---|---|---|
| P1: 中国用户用海外节点 | 已有（prod 在 us） | 多 region 部署后用户被自动路由到最近节点（ap-sea、ap-northeast-1） |
| P2: 海外用户用国产模型 | 已有（newapi 第五平台支持 kimi/ark/deepseek 等 channel_type） | 同 P1，能力本来就在；新增 region 不改变 |
| P3: 统一品牌 | 已有（一份前端） | 自动 |
| P4: 100% 全量脱敏落盘 | 未做 | 在 forward 链路插入 audit middleware（独立 design，与本方案正交但能受益于"一份实现 N region 一致"） |
| P5: 用户体系共享 | 已有（一份用户 schema） | 自动 |
| P6: OPC 友好 | 已有 §5/§5.y/§10 + cloud-agent 工作流 | **强化 cadence**：双周 upstream merge 由 cloud agent 主导 |

**关键点**：方案 X **不要求新增任何产品决策**，只要求把已有 stage0 部
署模板（`deploy-stage0-workflow.md`）水平复制到新 region。所有 IaC
（`cicd-oidc.yaml`、`deploy-stage0.yml`）已就位。

### 3.4 方案 X 对"上游漂移痛点"的具体回应

把"积累式手工合并"换成"双周机械 cadence"：

1. `scripts/check-upstream-drift.sh` 已经能给出 ahead/behind 数据
2. `.github/workflows/upstream-drift-monitor.yml` 已经周一自动开 issue
3. **新增**：cloud agent 双周自动开 `merge/upstream-YYYYMMDD` PR
   - 用 `git merge-tree upstream/main HEAD` 预跑冲突
   - 无冲突或仅文档冲突 → cloud agent 自己解决并 push
   - 有代码冲突 → 让人介入（保留 §5/§5.y 的人工审批门槛，因为冲突区
     必然涉及注入点判断）
4. new-api 同理：`.new-api-ref` bump 由 cloud agent 双周尝试，bump 后
   `make test` 全绿才提交

这是**用 OPC 杠杆解决 OPC 痛点**：增加 Agent 自动化频率，而不是减少代
码融合度。

### 3.5 方案 X 对"P4 全量审计"的具体回应

独立写一篇 `docs/approved/audit-redact-pipeline.md`，骨架（不在本文档
范围内详细展开，仅给方向）：

```
HTTP request → audit_capture middleware
    ├─ snapshot req headers/body (with key-aware redaction rules)
    ├─ tag with: account_id, group_id, model, request_id
    └─ async push → S3 (parquet daily partitions) + redis hot buffer

response stream → audit_capture middleware (split tee)
    ├─ tee chunks to redact pipeline
    └─ aggregate after stream close → S3
```

脱敏规则在一个 `audit_redact_rules.yaml` 里集中维护：API key、邮箱、
手机号、身份证、SSN、银行卡、自定义正则。

这一改动与拆栈与否完全无关；放在融合代码库里实现一次，所有 region 行
为一致，是 P4 "100%" 字面要求最稳的实现。

### 3.6 方案 X 对"地理分区/不同用户群"的具体回应

提议里的"中国用户群" vs "海外用户群"在融合代码库里通过**数据维度**而
非**部署维度**表达：

- `user.region_preference` 字段（user 默认绑定一个 region；可切换）
- `group.platform_preference`：例如某 group 优先调度 newapi 第五平台账
  号（kimi/ark/deepseek），另一个 group 优先调度 OAuth 订阅账号
- `account.region_affinity`：例如某些 OAuth 订阅账号的 IP 出口要在海
  外，调度时优先选这些账号

这些都是**单代码库 + schema 字段**就能表达的东西，不需要拆产品。

---

## 4. 不做的（聚焦过滤）

| 不做 | 原因 |
|---|---|
| 拆 sub2api / new-api 双栈 | §2 全部 Z1~Z5 |
| 跨境账户中心 | Z2 |
| "海外用户走亚太节点用国产模型"作为产品规划前提 | §1.3 用户分群假设证据不足；可作为**已存在能力的一个使用场景**，不必单独造拓扑 |
| 给 new-api 套 TokenKey logo | §2.3，表层装修，无法消除两套 admin UI 异构带来的体感断裂 |
| 推翻 `newapi-as-fifth-platform` | Z1，沉没设计资产不可回收 |
| 自建第三个"账户中心"服务 | Z2，复杂度大于现有融合 fork 维护成本 |

---

## 5. 推荐执行轮廓（非细节 design）

> 注意：本文档是**评估 + 方向**，不是可执行 design。每个子项达到要落
> 地阶段，需要单独走 `docs/approved/` 流程并得到 reviewer 审批。

### 5.1 立刻可做（无新风险，纯加强现状）

- **R1**：在本仓库继续按 §5.y.1 cadence 双周做 `merge/upstream-*` PR；
  cloud agent 尝试自动化无冲突合并
- **R2**：维持 `.new-api-ref` 双周 bump 节奏；bump 由 cloud agent 主导
- **R3**：`scripts/check-upstream-drift.sh` 加 `new-api` 维度（当前只
  检查 sub2api upstream，可同样检查 new-api pin 与 origin/main 的 ahead 距离）

### 5.2 中期需 design（有风险，需 `docs/approved/` 走流程）

- **D1**：`docs/approved/audit-redact-pipeline.md` —— P4 的完整 design
- **D2**：`docs/approved/multi-region-stage0.md` —— 把 stage0 模板从单
  region 复制到 N region 的 IaC 改造（共享主库、replica、Anycast/DNS）
- **D3**：`docs/approved/account-region-affinity.md` —— `user.region_preference`
  / `account.region_affinity` schema 与调度器的对接

### 5.3 长期可考虑（视真实流量决定，YAGNI 优先）

- **L1**：是否真的需要在 cn-* region 部署 TokenKey gateway。先看 P2
  的"海外用户用国产模型"是否在用户后台数据里被验证；没数据先不做
- **L2**：是否需要按合规要求做"中国境内用户数据本地化"——只在合规规
  则真的命中（注册用户超过某阈值、PIPL 触发）时再启动这件事

### 5.4 不会做（明确 say no）

- 拆双栈 / 跨服务账户同步 / new-api 单独换皮 / 推翻第五平台

---

## 6. 与 §5 / §5.y / §5.y.1 / sentinel 纪律的合规性

本提议（推荐方案）在融合架构内做事，天然继承现有所有合规检查：

| 现有纪律 | 本方案合规性 |
|---|---|
| §5 不删 upstream 符号 | ✅ 不涉及 |
| §5.x 默认保留 upstream 能力 | ✅ 强化保留（拆栈才是不保留） |
| §5.y 无历史重写 | ✅ 不涉及 |
| §5.y 上游合并 cadence | ✅ 显式强化 |
| §5.y.1 PR shape 检查 | ✅ 复用 |
| `scripts/newapi-sentinels.json` | ✅ 第五平台 sentinels 全部保留 |
| `scripts/preflight.sh` § 9 (newapi compat-pool drift) | ✅ 复用 |
| `scripts/preflight.sh` § 10 (sentinel registry) | ✅ 复用 |

原提议（拆双栈）则需要**作废**这一整套纪律——这是它"沉没成本"维度的
代价。

---

## 7. 决策清单（给审批人）

请明确回应以下四个问题再决定走 §5 推荐方案还是原提议：

1. **是否同意"上游漂移痛点的根因不是融合架构，而是合并 cadence"** —— 同意 → 走方案 X 的 R1~R3
2. **是否同意"100% 审计落盘与拆栈正交"** —— 同意 → 写 D1，不被架构选择绑死
3. **是否同意"海外用户用国产模型"作为产品前提**目前**证据不足** —— 同意 → 暂不投资 P2 拓扑
4. **是否同意"用户体系共享在两个独立服务里做对的代价 ≥ 维护融合 fork"** —— 同意 → 不做账户中心

如果四个问题都是 Yes，原提议的拆栈部分应被推翻，按 §5 推进。

如果某一项是 No，需要补充论据并重新评估——但应当**先回答这些问题**，
而不是先开始动 IaC。

---

## 8. 元反思（OPC 视角）

这份文档本身的写作过程也是 OPC 的一次实践：

- 用户给了一个含多个要素的提议，agent 没有立刻 "yes and" 去拆架构，而
  是先把现状（91 ahead / 257 behind / new-api pin / fifth-platform 资
  产）和提议拆成可独立评估的要素
- 评估结论里大方向 say no，但精确指出**提议命中的真问题**（上游漂移、
  审计、地理分区），并给出更便宜的解法
- 整个推荐方案的所有动作都复用已有 IaC、已有纪律、已有 sentinel——这
  是 OPC "杠杆最大化" 的样板

如果 reviewer 不同意本文档的结论，最有价值的反驳路径是：

- 给出 P2 海外用户用国产模型的真实流量数据（驳 §1.3）
- 给出"账户中心比融合 fork 更便宜"的工程量估算（驳 Z2）
- 给出"两套 admin UI 异构对用户体感影响很小"的用户访谈证据（驳 §2.3）

否则，应按 §5 推荐方案推进。
