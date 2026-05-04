# TokenKey 面向 OPC 的实操改造方案（乔布斯 / OPC 收敛版）

> 适用仓库：`tokenkey/sub2api`（本仓库）
>
> 相关现状基线：2026-04-25
>
> 目标：在**继续以 sub2api 为控制面基座、继续通过 Go module 引入 sibling `new-api` 能力**的前提下，把 TokenKey 演进成一个**统一品牌、统一用户体系、统一控制面、统一证据面**的 OPC 产品，并显著降低 `sub2api upstream` / `new-api upstream` 双上游带来的长期合并税。

---

## 0. 先给结论

这份方案经过再次反思后的结论更简单，也更苛刻：

**TokenKey 不该继续演化成一个“功能越来越多的大 Fork”，而该收敛成一个“对外只有一个产品、对内只有一条主骨架”的系统。**

如果用乔布斯和 OPC 的标准来判断，这个仓库未来应该只坚定做三件事：

1. **一个产品**：对外只有 TokenKey，没有“sub2api 产品 + new-api 产品”的双重心智。
2. **一条主骨架**：所有引擎路由决策逐步收口到 TokenKey 自己的最小编排层，而不是散落在热点 service 文件里。
3. **一个默认证据面**：所有请求轨迹都默认进入同一条“先脱敏、再持久化”的证据链路，而不是 error log / QA export / 局部 sanitize 三套并存。

这意味着我们不是去做“更多层”，而是去做**更少的概念、更少的判断点、更少的人工维护面**。

---

## 1. 乔布斯 / OPC 视角下，这份方案真正要解决什么

### 1.1 不是“怎么继续加功能”，而是“怎么让系统重新变简单”

如果按乔布斯的标准审视当前仓库，真正的问题不是功能不够，而是：

- 同一个产品背后已经有两套快速演化的上游
- 同一种路由判断分散在多个热点文件里
- 同一种证据需求被拆成多套半重叠实现
- 对内的技术实现细节仍在泄漏成对外产品心智

这会直接导致最反 OPC 的后果：

- 一件事要在多处修改
- 一次上游变化要重新判断很多次
- 一个事故发生后还要先判断“日志到底在哪”
- 一个产品却要靠解释才能让人理解

### 1.2 乔布斯哲学要求：先消灭复杂度，再谈扩展性

这意味着本方案不能以“抽象得更完整”为目标，而必须以“把复杂度从主路径拿掉”为目标。

所以这份方案的核心不是：

- 做一个宏大的平台中台
- 做一个覆盖一切的 catalog 系统
- 做一个到处都能接入的新框架

而是：

- 把 TokenKey 特有判断从 upstream 热点文件搬出去
- 把 `newapi` 从外部产品概念降级为内部实现细节
- 把证据采集从散点能力升级成系统默认行为

### 1.3 OPC 哲学要求：减少重复判断，把经验固化成门禁

OPC 不是“一个人扛更多复杂度”，而是“系统替人消灭重复判断”。

所以每一个已经反复踩过的坑，都应该被重新表达成：

- 一个 source of truth
- 一个 facade / registry
- 一个 preflight check
- 一个 CI gate
- 一个 fail-open 但可观测的默认行为

---

## 2. 先明确：TokenKey 不是什么

为了收敛方案，先明确不做什么。

### 2.1 TokenKey 不是 `new-api` 的 UI 套壳

TokenKey 的核心资产是控制面：

- 用户体系
- 分组与账号
- 配额与调度
- 支付与运营
- 管理端与统一品牌

如果未来产品叙事退化成“一个包着 `new-api` 的壳”，那就是战略后退。

### 2.2 TokenKey 不是两个产品的拼接说明书

用户不应该理解：

- 哪些能力来自 `sub2api`
- 哪些能力来自 `new-api`
- 哪些路径叫 compat，哪些路径叫 bridge

这些都应该是内部实现事实，不应成为主要产品心智。

### 2.3 TokenKey 不是一个越来越厚的定制 Fork

只要 TokenKey 特有逻辑继续堆进：

- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_account_scheduler.go`
- `backend/internal/service/gateway_bridge_dispatch.go`

那么每次 upstream 合并都会继续为过去的结构买单。

### 2.4 TokenKey 也不是一个“先原样记下来，以后再脱敏”的可观测系统

“100% 脱敏后持久化”的真实含义不是“先抓全，再处理”，而是：

- raw secret 不进入持久层
- raw secret 不进入结构化日志
- 持久化层只允许出现脱敏后的证据

这条不能退让。

---

## 3. 本方案必须同时满足的硬约束

### 3.1 产品与业务约束

1. **继续以 sub2api 为控制面基座，继续使用同一套用户体系。**
2. **sub2api upstream 继续提供 openai / anthropic / gemini / antigravity 四平台原生引擎。**
3. **new-api upstream 继续提供更广的扩展模型引擎，但不暴露为第二套独立产品。**
4. **对外统一为 TokenKey：统一品牌、统一入口、统一导出、统一运营心智。**
5. **所有请求轨迹必须在“完全脱敏之后”才能进入持久化层。**
6. **所有改造都必须服务于减少维护面，而不是增加概念数。**

### 3.2 仓库与实现约束

1. **控制面留在本仓库，不新开第二控制面 repo。**
2. **不在 sibling `new-api` 仓库中打 TokenKey 私有补丁来解决本仓库问题。**
3. **继续遵守当前 `CLAUDE.md` 的 merge discipline、preflight、sentinel、upstream merge shape 规则。**
4. **数据库仍然是 PostgreSQL only；Ent schema 仍然是数据模型事实源。**
5. **前端仍然使用 pnpm。**
6. **默认以单 PR、可回滚、可验证方式推进，不做一次性超大改造。**

---

## 4. 当前仓库真正的优势，不要浪费

这份方案不是推倒重来，而是基于当前已经做对的东西继续收敛。

### 4.1 控制面仍然在本仓库手里

当前用户、分组、账号、调度、网关、支付、后台都由本仓库控制，这一点是方向正确的。

关键落点：

- `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `backend/internal/service/account_tk_compat_pool.go`
- `backend/internal/service/openai_gateway_service_tk_newapi_pool.go`
- `backend/internal/relay/bridge/*`

### 4.2 `new-api` 已经被放进了正确边界

当前整合方式已经具备继续收敛的基础：

- `backend/go.mod` 使用 sibling replace
- `.new-api-ref` 固定 commit pin
- `scripts/sync-new-api.sh` 负责 sync / check / bump
- `backend/internal/integration/newapi/*` 与 `backend/internal/relay/bridge/*` 已形成边界

这说明现在最需要做的不是换方向，而是**把边界再变硬**。

### 4.3 仓库已经有 OPC 风格的机械护栏文化

关键机制已经存在：

- `scripts/check-upstream-drift.sh`
- `scripts/newapi-sentinels.json`
- `scripts/check-newapi-sentinels.py`
- `scripts/preflight.sh`
- `.github/workflows/upstream-merge-pr-shape.yml`

这很重要，因为说明本仓库适合继续把“靠记忆”的规则升级成“靠脚本”的规则。

### 4.4 已经有证据面的好积木

现有基础不是零：

- `backend/internal/util/logredact/redact.go`
- `backend/internal/service/ops_service.go`
- `backend/internal/observability/qa/service.go`

所以未来不是“新造一个 observability 产品”，而是把这些能力收口成一条主链路。

---

## 5. 收敛后的目标形态：不是三层大架构，而是一条产品主骨架

上一版把它表达成 `Control Plane / Engine Plane / Evidence Plane` 三层；这个表达仍然成立，但从乔布斯 / OPC 的角度，更准确的说法应该是：

> **TokenKey 只有一个产品骨架：Control Plane 负责产品控制，Engine Spine 负责路由编排，Evidence Spine 负责证据闭环。**

这里把 “Plane” 换成 “Spine”，目的是提醒自己：

- 不是新造三套系统
- 不是给系统再加一层组织结构
- 而是把最关键的主路径重新收束成一条骨架

### 5.1 Control Plane：继续做唯一产品入口

Control Plane 继续承载：

- 用户体系
- API Key / JWT / 权限
- 分组与账号管理
- 调度策略与配额
- 计费 / 支付 / 后台运营
- TokenKey 对外品牌与前后台 UI

**原则：用户永远只进入 TokenKey 控制面。**

### 5.2 Engine Spine：只负责“该走谁、能不能走、怎么保持真值唯一”

Engine Spine 只负责统一回答以下问题：

- 这个请求属于哪个逻辑平台
- 当前 endpoint 应该走 native 还是 bridge
- 哪些 `channel_type`、协议形态、endpoint 是合法组合
- 哪些平台 / endpoint / provider 能力是真值表，而不是散点 if/else

它**不负责**吞并全部 service 逻辑，不负责重写网关，不负责追求抽象完美。

### 5.3 Evidence Spine：只负责“默认留下脱敏后的完整证据”

Evidence Spine 只负责统一完成：

- 轨迹 ID 分配
- request / response / upstream event envelope
- payload 脱敏
- blob 写入
- metadata 存储
- 查询 / 导出 / retention / DLQ

它的原则不是“哪里需要哪里记”，而是：

**所有主路径请求默认留下脱敏证据；写入失败不阻断流量，但绝不 silent-loss。**

---

## 6. 设计原则：用更少的概念，解决更多的事

### 总裁判原则

> **任何改造，只有在它同时减少产品心智分裂、减少热点文件分叉、减少证据入口分裂、减少人工判断点时，才值得进入主线。**

如果一个设计只是“更完整”“更灵活”“更方便以后扩展”，但没有同时减少这四类复杂度，就不应进入主线。

### 原则 1：一个产品，多个能力来源，但不能有多个产品心智

- `newapi` 可以是内部 provider key
- 但 TokenKey 不能对外看起来像“第五个平台产品”
- 对外应该呈现统一能力入口，而不是底层来源说明
- `newapi` 不是一个要被解释给用户听的平台名，而只是内部能力来源标识

### 原则 2：热点文件只允许存在薄注入点，不允许继续长出业务主干

以下文件未来应该越来越薄：

- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_account_scheduler.go`
- `backend/internal/service/gateway_bridge_dispatch.go`

允许存在：

- facade 调用点
- 必要字段扩展
- 最小兼容注入

不应继续增长：

- TokenKey 特有真值表
- provider-specific fallback 规则
- operator-facing 解释与策略分支

### 原则 3：同一类事实必须只有一个 source of truth

以下事实必须被集中定义：

- compat 平台集合
- 调度池成员资格
- endpoint 到 provider 的判定规则
- capability truth table
- redaction key set / redaction policy version
- 默认品牌与展示词

进一步写死：

- 后端关于 `platform / endpoint / provider / channel_type` 的组合真值，只允许定义在统一 registry
- 前端不再自行定义业务真值，只允许消费后端投影或稳定枚举
- bridge / scheduler / route helper 不得再各自持有一份平台真值副本

### 原则 4：所有高频错误，都要变成机械门禁

凡是已经重复踩过的坑，都不再只留在文档和 review 里：

- 要么进入 `scripts/preflight.sh`
- 要么进入 CI workflow
- 要么进入 semantic checker / sentinel

### 原则 5：默认简单，扩展延后

如果某个设计能解决当前问题，但同时引入新的概念层、新的配置源、新的 operator 面板，那默认先不做。

先问三个问题：

1. 不做它，当前主问题能不能先解决？
2. 它是否让维护面增加而不是减少？
3. 它是否让一个人未来每周要做更多判断？

只要有一条答案不对，就延后。

### 原则 6：端到端体验优先于局部工程优雅

判断方案是否成立，不看它抽象得多漂亮，而看它是否让下面这些体验变简单：

- 用户是否更少感知底层实现
- operator 是否更少切换多个排障入口
- 开发者是否更少在 merge 时解决重复冲突
- 出问题时是否更快拿到完整证据

---

## 6.5 上游合并适应层：merge 不是债务登记，而是收敛演练

本方案不是只服务“从零重构”的完成态路线图，也必须指导每一次 upstream merge。

上游合并最容易把系统带回反 OPC 状态：热点文件变胖、truth table 变多、品牌词回退、证据入口绕开主链路。处理方式不能是“先合进去，记 follow-up”。对 OPC 而言，follow-up checklist 本身就是人工债务。

### 6.5.1 三类准入标准

#### Invariant：任何 PR 都不能破坏

以下不因“这是 upstream merge”而放宽：

- raw secret 不进入持久层或结构化日志
- 对外产品心智不从 TokenKey 回退到 Sub2API / New API
- `newapi` 不恢复成默认外部产品名，只保留为内部 identity
- 新主流量 endpoint 必须接入既有 trajectory / QA capture / redaction hooks
- 新 capability / routing truth 不得只靠散点 if/else 存在

#### Merge Harness：冲突解决 commit 的职责

上游 merge commit 只负责：

- 保留 upstream 能力，不 silent-delete
- 解决编译、生成代码、基础测试问题
- 把新增高风险入口先接入现有 canonical hooks
- 保证 preflight、sentinel、PR shape gate 能正常运行

它不负责把 TokenKey 的架构一次性重写好，也不应该把 TokenKey 私有重构混进冲突解决里。

#### OPC Refactor Commit：同一 PR 内的收敛 commit

当 upstream merge 引入或触碰后显著增厚分叉面，必须在同一个 PR 内用独立 commit 收敛，而不是记 follow-up：

- 热点文件新增的 TokenKey 分叉，迁到既有 companion / facade / 最小 Engine Spine
- 平行 truth table，收敛到单一代码 owner
- 大型 UI 文件新增的策略面板，抽到专用 component / composable
- 新 sentinel 不是“字还在”，而是验证关键行为仍被关键路径调用

边界也必须收紧：只处理本次 merge 引入或触碰后显著增厚的分叉面；历史债务不借 merge PR 扩张。

### 6.5.2 决策矩阵

| upstream 带来的变化 | merge commit 动作 | OPC refactor commit 动作 | 必要门禁 |
|---|---|---|---|
| 新主流量 endpoint | 接入现有 auth / body limit / trajectory / QA hooks | 若出现重复 route predicate，迁到 canonical helper | route / trajectory sentinel |
| 热点文件新增 TokenKey 分支 | 保真解决冲突 | 移入 companion / facade，仅留薄调用点 | semantic call-site check |
| 新 capability / probe truth | 保留能力与测试 | 收敛到单一代码 owner | owner + sentinel |
| 新 operator UI 策略块 | 保留可用 UI | 抽 component / composable，主 view 只 wiring | frontend test / lint |
| 品牌或内部术语外泄 | 当场修复 | 必要时补 brand sentinel | brand drift check |

### 6.5.3 PR #110 作为演练基线

PR #110 不只是一次 upstream merge，也是这套纪律的第一次演练：

- **Merge Harness commit**：保留 upstream 新增能力与历史审计链，只做语义保真、冲突解决、编译与基础测试修复。
- **Invariant commits**：修复不可退让项，例如 Responses canonical route、TokenKey 支付心智、QA compressed / multipart evidence、locale key migration、raw sticky debug logs 移除、`AGENTS.md` 可追踪性。
- **OPC Refactor commits**：只处理本次 merge 引入或触碰后显著增厚的分叉面，例如 gateway / OpenAI capability truth、Settings fast/flex policy UI、Vertex location truth 的 owner 判定。

PR #110 的边界也必须明确：

- 不把历史上所有 hotspot debt 都塞进这次 merge PR。
- 不把“未来会重构”写成全局长期债务来替代当前可收敛的分叉。
- 不把 upstream 能力删除来换取简单；正确动作是保留能力，再把 TokenKey 分叉迁回最小 owner。
- 不把 mechanical gate 写成文档愿望；owner 必须体现为代码入口与 semantic sentinel。

这让“上游合并”本身成为持续降低 merge tax 的机制，而不是制造新债务的入口。

---

## 7. 对本仓库的实操改造方案

## 7.1 改造一：建立最小 Engine Spine，而不是宏大 Engine Framework

### 目标

把“native 四平台 + new-api 扩展能力”的路由与能力差异，统一收口到 TokenKey 自己的最小编排骨架，而不是散在多个 gateway / service 文件里。

### 核心反思

上一版里最容易继续膨胀的点，是把 Engine Plane 做成一个过度完整的引擎层。这个方向不够乔布斯，也不够 OPC。

**真正应该做的是最小 Engine Spine：只先解决“该走谁”和“真值表在哪”。**

### 建议新增目录

```text
backend/internal/engine/
├── facade.go
├── provider.go
└── dispatch_plan.go
```

第一阶段不预先承诺 `capability.go` / `registry.go` / `errors.go`。原因不是这些文件永远不需要，而是目录一旦先写全，执行时就会自然滑向“把框架补完整”，而不是先把主路径收口。

### 第一阶段只做这三件事

1. 新增 `facade.go`
2. 新增 `dispatch_plan.go`
3. 把最主要的 provider 选择路径迁到 facade

优先落点：

- `backend/internal/service/gateway_bridge_dispatch.go`
- `backend/internal/service/openai_gateway_bridge_dispatch.go`
- `backend/internal/service/openai_gateway_service.go`

目标不是重写逻辑，而是把这些文件里的 TokenKey 分叉判断变成：

```go
plan := engineFacade.ResolvePlan(...)
return engineFacade.Execute(ctx, plan)
```

### 第二阶段再做 capability truth table 集中化

在 facade 稳定后，再新增：

```text
backend/internal/engine/
├── capability.go
└── registry.go
```

用来逐步收口这些散点事实：

- OpenAI-compatible 平台集合
- endpoint 是否走 native / bridge
- image / video / task fetch 与 `channel_type` 的关系

当前这些事实分散在：

- `backend/internal/service/account_tk_compat_pool.go`
- `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `backend/internal/relay/bridge/video_relay.go`
- `backend/internal/service/openai_gateway_bridge_dispatch_tk_video.go`
- `backend/internal/integration/newapi/channel_types.go`

### 为什么这更符合乔布斯 / OPC

因为它不追求“架构完整”，只追求“主路径冲突面更小”。

这是真正的聚焦。

---

## 7.2 改造二：把 `newapi` 从外部产品词降级为内部实现词

### 目标

满足“统一平台、统一视觉、统一形象”，但不破坏现有 routing identity。

### 边界

- **内部保留**：`group.platform = newapi`、`PlatformNewAPI`、`channel_type`、bridge adaptor 等术语
- **外部默认不主打**：`newapi` 不再作为最终用户或多数 operator 的主展示词

### 建议改法

#### A. 先完成 TokenKey 默认品牌替换

按已有 `docs/ui/tokenkey-brand-replacement-plan.md` 执行，并补齐这些位置：

- `frontend/src/stores/app.ts`
- `frontend/src/router/title.ts`
- `frontend/src/i18n/locales/en.ts`
- `frontend/src/i18n/locales/zh.ts`
- `frontend/index.html`
- `backend/internal/web/frontend_spa.go`
- `backend/internal/service/setting_service.go`
- `README.md` / `README_CN.md` / `README_JA.md`

#### B. admin UI 中把平台展示分成“产品词”和“技术词”两层

建议：

- internal key 仍然是 `newapi`
- 默认 UI label 改为：
  - 中文：`扩展引擎`
  - 英文：`Extension Engine`
- 仅在高级 tooltip / debug / operator 深层信息中显示底层 provider 为 `newapi`

优先修改：

- `frontend/src/constants/gatewayPlatforms.ts`
- `frontend/src/composables/usePlatformOptions.ts`
- 所有平台下拉 / segmented control / badge 的 label 来源

### 品牌层的硬边界

- 品牌替换只发生在展示层
- 不得为了 UI 统一去重命名路由 identity、平台常量、`group.platform` 存量语义
- 展示层收口是低风险品牌动作；协议层、调度层、路由层 identity 变更是另一类高风险改造，不能混做


---

## 7.3 改造三：先做 Capability Truth Table，再考虑 Catalog

### 为什么要再收紧

上一版把 catalog 提得还是偏前。按乔布斯 / OPC 判断，这仍然太早。

现在更清晰的结论是：

- **先做内部真值表**
- **后做对外展示目录**

如果内部 capability source of truth 还没稳定，就不应该过早做大 catalog。

### 第一阶段：内部 capability truth table

建议由 `backend/internal/engine/capability.go` 与 `registry.go` 统一输出至少这些事实：

- model / endpoint 对应的逻辑平台
- backing provider（native / bridge）
- protocol shape
- supported endpoints
- 是否依赖 `channel_type`
- 是否支持 image / video / task fetch

### 第二阶段：如有必要，再做 catalog 视图

如果未来 operator / UI 确实需要统一展示，再新增：

```text
backend/internal/engine/catalog/
├── model_catalog.go
├── provider_catalog.go
├── sync.go
└── snapshot.json   # 可选
```

但注意：

**Catalog 不是主线，只是主线稳定后的视图层。**

---

## 7.4 改造四：把“100% 脱敏后持久化”变成唯一默认路径

这是整份方案里最不能妥协的一块。

### 当前已有基础

- `logredact.RedactJSON / RedactText`
- `OpsService.RecordError` 的脱敏裁剪路径
- `observability/qa/service.go` 的 request / response / blob / zstd / export 能力
- PR #81 之后，主 gateway 路径已经具备 `trajectory_id` 分配、request / response / SSE chunk capture、脱敏 blob 写入、`qa_records` 元数据与用户侧基础导出

### PR #81 之后的关键边界

当前系统已经向统一证据面推进了一步，但落盘本质仍是：

**一条请求，对应一条脱敏后的请求级 evidence record。**

它服务事故排查、用户自助导出、主路径可观测性，也是后续 Evidence Spine 的事实源。

它还不是：

- 一个 session 主实体
- 一个 turn 序列
- 一个结构化 tool-use 数据集
- `traj 标准 v1.0` 要求的 session / turn / tool-use JSONL

因此必须保持两层：

1. **请求级 Evidence Spine**：生产事实源，负责主路径捕获、脱敏、request / response / stream 持久化、fail-open + DLQ + metrics。
2. **session 级 traj Projection**：派生视图，从 evidence 记录组装 session、提取 turn、结构化 tool schema / call / result，并执行 H1/H2/H3/D1 验收。

不能为了追求 `traj 标准 v1.0` 一次到位，把主写路径改成大 session 文档，也不能在 gateway 热点 service 中维护 session 状态。

### 当前真正缺口

- 请求级 evidence 还未完全升级为统一 trajectory record schema
- QA / ops / gateway 仍然是并行证据体系，尚未全部关联到唯一 evidence spine
- capture 完整率不是系统级指标
- redaction policy 还未成为版本化契约
- session 级 traj projection 尚未具备默认 `session_id`、turn 结构、tool-use 三件套、标准导出与验收门禁

### 目标状态

所有进入 TokenKey 的请求，不论：

- 成功 / 失败
- 流式 / 非流式
- native / bridge
- OpenAI-compatible / Anthropic-compatible

都形成同一条 trajectory，并且：

**只有脱敏后的证据可以进入持久化层。**

### 建议新增目录

```text
backend/internal/observability/trajectory/
├── capture.go
├── redaction.go
├── writer.go
└── metrics.go
```

第一阶段不预先承诺 `blob_store.go` / `dlq.go` / `export.go` / `envelope.go`。这些能力可能会需要，但不应在 M1 一开始就把 Evidence Spine 写成完整产品面。

如果按第一阶段的目标收口，M1 只需要先证明三件事：

1. 每个主路径请求都有 `trajectory_id`
2. request / response 在脱敏后可被稳定持久化
3. capture 失败可追、可计数、不可 silent-loss

### 建议新增数据模型（Ent schema）

```text
backend/ent/schema/trajectoryrecord.go
backend/ent/schema/trajectoryevent.go   # 可选
```

### `trajectory_records` 建议字段

- `trajectory_id`
- `request_id`
- `user_id`
- `group_id`
- `api_key_id`
- `platform`
- `provider`
- `account_id`
- `channel_type`
- `endpoint`
- `model`
- `status_code`
- `success`
- `streaming`
- `request_sha`
- `response_sha`
- `request_blob_uri`
- `response_blob_uri`
- `stream_blob_uri`
- `redaction_version`
- `capture_status`
- `created_at`

### payload 持久化策略

- 元数据入 PostgreSQL，便于筛选、聚合与导出鉴权
- 大块 payload 入对象存储或本地 blob 存储，统一 zstd 压缩
- blob 内容永远是**脱敏后的 payload**
- `redaction_version` 必须随记录一起保存，确保以后能审计

### 统一 capture 入口建议

#### A. 入口 middleware 只建立上下文

新增：

- `backend/internal/middleware/trajectory_context.go`

职责：

- 生成 `trajectory_id`
- 把 user / group / key / endpoint 基础上下文写入 gin context

#### B. 各 gateway service 统一调用 capture API

统一调用：

```go
trajectory.CaptureStart(...)
trajectory.CaptureSelectedAccount(...)
trajectory.CaptureUpstreamEvent(...)
trajectory.CaptureFinish(...)
```

这样：

- `GatewayService`
- `OpenAIGatewayService`
- `GeminiMessagesCompatService`
- `AntigravityGatewayService`
- newapi bridge 路径

都走同一条证据骨架。

#### C. 流式块统一 capture

对 SSE / streaming response：

- 记录 chunk 接收时间
- 对 chunk 做脱敏
- 聚合或流式写 blob
- 最终绑定到同一 `trajectory_id`

这里可以借鉴 `observability/qa/service.go` 的 blob 与 chunk 思路，但必须升级成通用主链路，而不是 QA 私有实现。

### 运行策略

- 主请求路径：**异步写入，fail-open**
- 写入失败：进入 DLQ + 计数器 + 结构化错误日志
- 定时任务：补偿 DLQ、统计 capture completeness、输出告警阈值

### traj projection 策略

`traj 标准 v1.0` 需要的数据集结构只从 evidence 派生，不反向驱动主路径变胖：

- `session_id` 不依赖特定 synth pipeline 才存在
- session / turn / tool-use 组装逻辑落在 projection / exporter 层
- `system prompt`、tool schema / call / result 是数据集字段，不代表要新增 prompt 平台或 Agent runtime
- H1/H2/H3/D1 验收由 exporter / CI 负责，不进入 gateway 热点路径

### 为什么这符合乔布斯 / OPC

因为它做的不是“更多日志能力”，而是“唯一默认路径”。

一个系统是否简单，不看它有没有很多工具，而看它遇到真实问题时，大家是否本能地知道该去哪里找证据。

---

## 7.5 改造五：把上游 merge / bump 动作彻底产品化

### 目标

让 fork 维护不再主要依赖操作者经验。

### 建议新增脚本 1：上游合并助手

```text
scripts/prepare-upstream-merge.sh
```

职责：

- 自动运行 `git fetch upstream origin`
- 输出 ahead / behind
- 自动创建 `merge/upstream-YYYYMMDD`
- 跑 `git merge-tree upstream/main HEAD`
- 生成 PR body 草稿到临时文件，包含：
  - `git log --oneline upstream/main..HEAD | wc -l`
  - `git diff --stat upstream/main..HEAD -- backend/ | head -5`
  - conflict hotspots
  - sentinel / checklist

### 建议新增脚本 2：new-api bump 助手

```text
scripts/bump-new-api.sh
```

职责：

- 接收目标 SHA
- 调用 `scripts/sync-new-api.sh --bump <sha>`
- 自动跑最小 smoke：
  - `go build ./...`
  - compat provider registry tests
  - bridge image / video tests
- 输出 bump 摘要

### 建议新增 CI 任务

#### A. Weekly upstream dry-run merge

目的不是立即合并，而是提前暴露冲突热点和 sentinel 漏洞。

#### B. Weekly new-api pin drift compile check

目的不是自动 bump，而是提前知道最近的 `new-api` upstream 变化会不会打断 bridge。

### 建议升级 sentinel：从 token presence 走向 semantic call-site

下一步必须补上的不是更多 token，而是更多语义约束：

- 某 helper 不仅存在，而且仍被关键路径调用
- 某 route helper 不仅定义，而且仍被注册
- 某 capture hook 不仅存在，而且仍接入 terminal path

这才真正符合 OPC：

**系统关心的不是“字还在不在”，而是“行为还在不在”。**

### 建议新增门禁对象

不是“helper 还在”就算通过，而是必须开始检查关键行为是否仍然接入主路径：

- `gateway_bridge_dispatch.go` 必须调用 facade
- `openai_gateway_bridge_dispatch.go` 必须调用 facade
- `openai_gateway_service.go` 的主要 provider 选择路径必须调用 facade
- 关键 gateway terminal path 必须存在 `CaptureFinish`
- 新 endpoint 若进入主流量路径但未建立 `trajectory_id`，preflight / CI 直接失败
- redaction key set 变化若未同步 bump `redaction_version`，直接失败

这类检查的目标不是证明“字还在”，而是证明“行为还在”。

---

## 8. 实施顺序再收敛：只保留 3 条主线，不再让路线图发散

上一版的 4 个阶段还是有点像“功能目录”。

按照乔布斯 / OPC 的标准，现在更推荐把它收敛成 **3 条主线**：

## 主线 A：品牌与自动化基线（先做）

### 目标

先把产品叙事和 fork 维护方式收紧，不碰主流量语义。

### 必做产出

1. `Sub2API -> TokenKey` 默认品牌替换
2. `newapi -> 扩展引擎 / Extension Engine` 的默认展示降级
3. `scripts/prepare-upstream-merge.sh`
4. `scripts/bump-new-api.sh`
5. weekly upstream dry-run / weekly new-api compile-smoke CI
6. brand drift check

### 明确不做

- 不改动调度语义
- 不改动 gateway dispatch 行为
- 不做 Ent schema 变化

### 验收标准

- 默认 UI / README / title / setting 文案不再出现 `Sub2API`
- operator 默认界面不再把 `newapi` 当外部产品名展示
- merge / bump 有固定脚本与固定 smoke

---

## 主线 B：最小 Engine Spine（第二优先级）

### 目标

先降低双上游合并税，而不是先追求大而全的抽象架构。

### 必做产出

1. 新增 `backend/internal/engine/facade.go`
2. 新增 `backend/internal/engine/provider.go`
3. 新增 `backend/internal/engine/dispatch_plan.go`
4. `gateway_bridge_dispatch.go` 改走 facade
5. `openai_gateway_bridge_dispatch.go` 改走 facade
6. `openai_gateway_service.go` 中最主要的 provider 选择路径改走 facade
7. semantic sentinel 第一版

### 明确不做

- 不重写全部 scheduler / gateway service
- 不建设完整 catalog 系统
- 不做过度抽象的统一 runtime

### 验收标准

- provider 选择由 facade 统一完成
- 热点 upstream 文件新增 TokenKey 分叉逻辑显著减少
- newapi image / video / chat / responses 路径无回归
- sentinel 能检查关键 helper 仍被关键路径调用

---

## 主线 C：Evidence Spine（第三优先级，但战略价值最高）

### 目标

把“100% 脱敏后持久化”从原则变成系统默认事实。

### 两段式边界

主线 C 不直接把生产主路径改造成 session 数据集写入器：

- **C-1 请求级 Evidence Spine**：生产事实源，保证所有主路径请求先脱敏、再持久化、可追踪、可补偿。
- **C-2 session 级 traj Projection**：从 evidence 派生符合 `traj 标准 v1.0` 的 session / turn / tool-use 数据集。

### 必做产出

1. `trajectoryrecord` Ent schema
2. `backend/internal/observability/trajectory/*`
3. 网关入口统一分配 `trajectory_id`
4. 先覆盖 OpenAI-compatible 主路径
5. redaction version 机制
6. 异步 writer + DLQ + 基础 metrics
7. 后续逐步把 QA export / ops_error_logs 关联到 unified evidence
8. traj projection / exporter / H1-H2-H3-D1 验收门禁

### 明确不做

- 不要求第一版就统一所有后台查询页面
- 不要求第一版就把全部历史导出视图重做
- 不要求第一版就构建复杂 retention 产品能力
- 不在 gateway / service 热点文件中维护 session 状态
- 不让 `traj 标准 v1.0` 的字段要求反向制造新的产品概念

### 验收标准

- 抽样主路径请求 100% 能追到 trajectory
- 持久化 payload 全部为脱敏后内容
- capture 失败不会阻断主请求，但一定留下 DLQ、计数和日志
- operator 在真实事故中能从统一入口拿到完整证据
- traj 导出能从 evidence 派生出可解析的 session / turn / tool-use 数据集

---

## 9. 每一条主线都必须补上的测试与门禁

## 9.1 品牌与自动化主线

必须新增：

- brand drift check
- merge helper smoke check
- new-api bump helper smoke check

## 9.2 Engine Spine 主线

必须新增：

- provider registry 单测
- dispatch plan truth-table 单测
- newapi / native provider smoke tests
- semantic sentinel call-site tests

必须新增门禁：

- `engine hook check`：主 dispatch path 必须走 facade
- `compat truth-source check`：OpenAI-compatible truth table 不得回退为散点定义

## 9.3 Evidence Spine 主线

必须新增：

- redaction property tests
- request / response / blob roundtrip tests
- streaming chunk capture tests
- fail-open + DLQ tests
- export authorization tests
- session assembly tests
- tool schema / call / result pairing tests
- traj JSONL parse tests
- H1/H2/H3/D1 acceptance tests

必须新增门禁：

- `trajectory hook check`：新 endpoint 未接入 capture 直接 fail
- `redaction version check`：敏感键集合变化必须带版本更新
- `terminal event check`：主路径必须有 terminal capture call
- `traj projection check`：session / turn / tool-use 导出不满足验收阈值直接 fail

---

## 10. 这套方案为什么现在更符合乔布斯和 OPC

### 10.1 它更聚焦了

从“很多方向都想做”收敛成了三条真正主线：

- 品牌与自动化
- 最小 Engine Spine
- 唯一 Evidence Spine

不是把事情做多，而是把主问题做穿。

### 10.2 它更端到端了

这份方案现在不是按“模块列表”组织，而是按真实产品体验组织：

- 用户看到什么
- operator 如何理解系统
- 开发者如何 merge
- 出事故时如何找到证据

这更接近乔布斯式思维：从整体体验回推系统形态。

### 10.3 它更像 OPC 了

因为它持续在做同一件事：

**把每周都会重复发生的人类判断，变成更少的判断点和更多的机械门禁。**

### 10.4 它更少自我感动式抽象了

现在明确把以下东西降级为“后续视图层”，而不是主线：

- 大 catalog 系统
- 复杂 operator 产品面
- 一开始就追求完美的引擎抽象

这能显著降低方案变胖的风险。

---

## 11. 明确不建议做的事

### 不建议 1：把 TokenKey 变成 `new-api` 的 UI 套壳

这是战略倒退。

### 不建议 2：继续把大量 TokenKey 语义直接塞进 upstream 主文件

这是把未来每一次 merge 的痛苦继续固化。

### 不建议 3：把“全量脱敏落盘”继续拆散在 error log / QA / 某个 handler patch 中

这样永远得不到唯一证据入口。

### 不建议 4：在 sibling `new-api` 中直接打 TokenKey 定制补丁

这样会把双上游问题升级成三处同步维护。

### 不建议 5：把 catalog、operator 视图、抽象完美感，放在主骨架之前

任何会让主线变慢、概念变多、判断点变多的东西，都应该延后。

---

## 12. 最终推荐：实际推进顺序

如果只给一个落地建议，我的建议是：

1. **先做品牌与自动化主线**：让产品叙事和 fork 维护先变简单
2. **紧接着做最小 Engine Spine**：让双上游差异从热点文件里退出
3. **然后做请求级 Evidence Spine**：让脱敏请求证据成为系统默认事实源
4. **最后做 session 级 traj Projection**：从 evidence 派生符合 `traj 标准 v1.0` 的数据集

只有这些主线稳定之后，才考虑：

- catalog 视图
- 更完整的 operator 证据产品面
- 更丰富的能力展示层

因为对 OPC 来说，**先把骨架做对，胜过把外围做满。**

---

## 13. 可以直接开干的首批 PR 清单

### PR-A1：默认品牌替换

- 完成默认品牌替换为 TokenKey
- 覆盖 UI title / setting / README 等默认产品词
- 不触碰平台 identity、路由 identity、调度语义

### PR-A2：`newapi` 外显降级

- admin UI 中将 `newapi` 默认展示为“扩展引擎 / Extension Engine”
- tooltip / debug / 深层 operator 信息中仍可显示底层 provider 为 `newapi`
- 不修改 `PlatformNewAPI`、`group.platform`、`channel_type`、bridge identity

### PR-A3：merge / bump 自动化基线

- 新增 `scripts/prepare-upstream-merge.sh`
- 新增 `scripts/bump-new-api.sh`
- 新增 weekly dry-run / compile-smoke CI
- 新增 brand drift check

### PR-B1：Engine Facade 引入

- 新增 `backend/internal/engine/facade.go`
- 新增 `backend/internal/engine/provider.go`
- 新增 `backend/internal/engine/dispatch_plan.go`
- 暂不引入 capability registry

### PR-B2：关键 dispatch path 接入 facade

- `gateway_bridge_dispatch.go` 改走 facade
- `openai_gateway_bridge_dispatch.go` 改走 facade
- `openai_gateway_service.go` 的主要 provider 选择路径改走 facade
- 补关键路径单测

### PR-B3：Capability truth table 收口

- 新增 `backend/internal/engine/capability.go`
- 新增 `backend/internal/engine/registry.go`
- 收口 compat 平台集合、endpoint/provider 判定、image/video/task fetch 与 `channel_type` 真值
- 新增 semantic sentinel 第一版

### PR-C1：Trajectory M1 最小落地

- 新增 `trajectoryrecord` Ent schema
- 网关入口分配 `trajectory_id`
- 先覆盖 OpenAI-compatible 主路径
- 持久化脱敏后的 request / response 元数据与最小 payload 证据

### PR-C2：Capture fail-open 护栏

- 补 async writer
- 补 DLQ 最小实现
- 补基础 metrics
- 建立 capture failure 可追踪、可计数、不可 silent-loss 的最小闭环

### PR-C3：Streaming 与统一证据收口

- 补 streaming chunk capture
- 让 QA export 逐步复用 unified evidence schema
- 让 `ops_error_logs` 关联 `trajectory_id`
- 为后续 export / query / retention 打基础

### PR-C4：Session / Turn Projection

- 从请求级 evidence 组装 session
- 生成默认 `session_id`
- 提取 turn / role / message kind
- 不在 gateway 热点文件维护 session 状态

### PR-C5：traj 标准导出器

- 输出符合 `traj 标准 v1.0` 的 JSONL
- 结构化 tool schema / call / result 三件套
- 支持 single-call 数据分组与去重

### PR-C6：traj 验收与门禁

- H1：有效轮次 ≥ 2
- H2：结构化工具调用 ≥ 1
- H3：工具配对率 > 0.3
- D1：精确+子集去重率 < 20%
- JSONL / JSON parse、session 完整性、tool 命名规范检查

---

## 14. 成功标准

当以下条件同时满足时，可以认为 TokenKey 已从“高维护 fork”升级为“乔布斯 / OPC 风格的可持续产品骨架”：

1. 对外默认品牌只剩 **TokenKey**。
2. 用户与大多数 operator 不需要理解 `sub2api` / `new-api` 才能使用系统。
3. `sub2api upstream` 合并时，热点冲突文件数量显著下降。
4. `new-api` bump 有固定脚本 + smoke，不再依赖人工经验。
5. 主路径请求都能查到对应 `trajectory_id` 与脱敏后的 evidence blob。
6. evidence capture 失败不会阻断主流量，但会留下 DLQ、指标与告警。
7. traj 导出不是 `qa_records.jsonl` 的薄包装，而是具备 `session_id / turn / tool-use` 的派生投影。
8. traj 数据集可通过 H1/H2/H3/D1 验收。
9. 新增 provider / endpoint 时，主要改动集中在 facade / registry / capture / exporter，而不是十几个 service 文件散改。

---

如果把整份方案压缩成一句话，那就是：

**让 TokenKey 成为唯一产品，让 sub2api 成为控制面基座，让 new-api 成为能力来源，让请求级脱敏 evidence 成为事实源，并从它派生标准化 traj 数据。**
