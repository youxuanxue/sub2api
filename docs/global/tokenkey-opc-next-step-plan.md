# TokenKey OPC 下一步执行计划（PR #81 之后）

> 适用仓库：`tokenkey/sub2api`
>
> 基线：`main` 已合并 PR #81 `Unify trajectory capture and TokenKey platform labeling`
>
> 相关主方案：`docs/global/tokenkey-opc-transformation-plan.md`
>
> 本文目标：在不偏离主方案的前提下，给出 PR #81 之后的实际推进顺序，并明确当前 traj 落盘与 `../traj/docs/traj 标准 v1.0.pdf` 的差距与补齐路径。

---

## 0. 先给结论

PR #81 做对了一件重要的事：

**它把 TokenKey 主路径里的 trajectory capture 往“统一证据面”推进了一步，但当前落盘仍是“请求级脱敏证据”，还不是 `traj 标准 v1.0` 所要求的“session / turn / tool-use 结构化数据集”。**

所以，下一步不应把系统拉向一个更胖的 observability 产品，也不应为了追 traj 标准而把主路径一次性改造成一套大而全的对话存储框架。

按乔布斯 / OPC 哲学，正确做法是：

1. **先继续收紧品牌与自动化基线**，让产品心智和 fork 维护方式先变简单。
2. **再推进最小 Engine Spine**，优先减少和 upstream 的热点冲突面。
3. **然后把 Evidence Spine 分成两层推进**：
   - 先完成生产级请求证据主链路；
   - 再从这条主链路派生出符合 traj 标准的 session 数据集投影。

一句话概括：

**请求级 evidence 继续做事实源；session 级 traj 不直接侵入主写路径，而作为派生视图与导出层补齐。**

---

## 1. 这份计划遵循的裁判原则

### 1.1 一个产品，不增加第二套心智

对外只有 TokenKey；对内只有一条更清晰的主骨架。

这意味着：

- 不新增一套“专供 traj 的独立产品面”
- 不让 operator 学两套证据入口
- 不把 `newapi` / `sub2api` / `traj` 的内部概念直接暴露成更多产品词

### 1.2 先消灭热点分叉，再补更多能力

对当前仓库而言，减少 upstream 冲突面的优先级高于“更完整的抽象”。

这意味着：

- 优先做薄注入点和 companion 文件
- 优先把判断收口到 facade / helper / registry
- 不把更多 TokenKey 特有逻辑继续塞进 upstream 热点文件

### 1.3 证据主链路与数据集视图必须分层

`traj 标准 v1.0` 要求的是可验收的数据集结构；生产系统首先需要的是稳定、脱敏、fail-open 的主链路证据。

因此必须明确：

- **请求级 evidence**：生产真相源（source of truth）
- **session 级 traj**：从 evidence 组装出的派生视图

不能为了追求一次到位，把二者硬焊成一层。

### 1.4 所有反复踩坑的点，都要变成机械门禁

后续 PR 只要涉及以下变化，就必须同步带检查：

- 新 gateway terminal path 是否接入 trajectory
- 敏感字段集合变化是否 bump `redaction_version`
- 关键 dispatch path 是否仍通过 facade
- traj exporter 是否仍满足 session / tool-use 验收阈值

---

## 2. PR #81 之后的代码事实

当前 trajectory / QA capture 的主写路径已经存在，关键落点如下：

- `backend/internal/server/middleware/client_request_id.go`
- `backend/internal/server/routes/gateway.go`
- `backend/internal/observability/qa/sse_tee.go`
- `backend/internal/observability/qa/service.go`
- `backend/internal/observability/trajectory/writer.go`
- `backend/ent/schema/qa_record.go`

### 2.1 当前已经成立的能力

当前系统已经能做到：

1. 主 gateway 路径默认分配 `trajectory_id`
2. 采集 request / response / SSE chunk
3. 先脱敏，再写 blob
4. 元数据入 PostgreSQL（`qa_records`）
5. blob 写入对象存储或本地 DLQ
6. 用户侧存在基础导出路径（`/api/v1/users/me/qa/export`）

### 2.2 当前落盘的本质

当前落盘的本质是：

**一条请求，对应一条脱敏后的证据记录。**

它服务的是：

- 事故排查
- 用户自助导出
- 主路径可观测性
- 后续统一 Evidence Spine 的最小骨架

它还不是：

- 一个 session 主实体
- 一个 turn 序列
- 一个结构化 tool-use 数据集

---

## 3. 对照 `traj 标准 v1.0` 的结论

结论必须明确：

**当前落盘不符合 `traj 标准 v1.0`。**

原因不是“没有任何基础”，而是**层级不对**：当前是请求级证据；标准要求的是 session 级对话数据。

### 3.1 已具备的基础原料

当前已经具备以下可复用原料：

- `trajectory_id`
- `request_id`
- `requested_model` / `upstream_model`
- `created_at`
- request body / response body / stream chunks
- `tool_calls_present`
- token usage
- 用户与 API key 归属信息

这些原料足以支撑后续组装，但还不是标准结构本身。

### 3.2 与标准的主要差距

#### A. 缺少默认 `session_id`

标准要求：

- `session_id` 必需
- 以 session 为去重与验收单位
- 从 user 首条到 assistant 末条完整闭环

当前只有：

- `trajectory_id`（单请求相关 ID）
- `request_id`
- `synth_session_id`（仅特定 pipeline 标签）

问题：

- 还没有系统默认的 session 主键
- 还没有多请求拼 session 的机制

#### B. 缺少 turn 级消息结构

标准要求：

- user → assistant
- assistant → tool → result
- 用户追问

当前只有单次 HTTP request / response 证据，没有：

- `turn_index`
- `role`
- `messages[]`
- turn 间边界
- 对话闭环判定

#### C. 缺少结构化 tool-use 三件套

标准要求同时具备：

- tool schema
- tool call
- tool result

当前只有：

- `tool_calls_present` 布尔值
- 原始 request / response blob

问题：

- 没有结构化抽取 tool schema
- 没有 tool call / tool result 配对
- 不能计算可靠的工具配对率

#### D. 缺少 `system prompt`

标准将 `system prompt` 视为必需字段；当前未落盘为稳定字段。

#### E. 缺少 session 级导出与验收门禁

标准要求：

- H1/H2/H3/D1 指标
- JSONL / JSON 可解析
- session 拼接、去重、抽检流程

当前导出仍是 `qa_records.jsonl`，属于请求记录导出，不是 traj session 导出。

---

## 4. 关键决策：不要把主写路径直接改成“大 session 文档”

这是下一阶段最重要的架构约束。

### 4.1 不建议的方向

不建议直接把主路径 capture 改成：

- 每个 session 一条超大 JSON
- 一次请求写入并修改整个 session 文档
- 在 gateway 热点 service 中显式维护会话拼接状态
- 为了数据集格式，把生产请求链路变成对话存储引擎

这样会带来：

- 更大的写放大和状态复杂度
- 更多与 upstream 热点文件的耦合
- 更胖的 request path
- 更难的 fail-open 与排障语义

### 4.2 建议的方向

保持两层：

#### 第 1 层：请求级 Evidence Spine

负责：

- 主路径捕获
- 脱敏
- request / response / stream 持久化
- fail-open + DLQ + metrics

#### 第 2 层：session 级 traj Projection

负责：

- 从 evidence 记录组装 session
- 提取 turn
- 结构化 tool schema / call / result
- 导出符合 traj 标准的 JSONL
- 执行 H1/H2/H3/D1 验收

这样更符合乔布斯 / OPC：

- 生产主链路只做最少必要的事
- 数据集层作为视图，不污染主路径
- 新需求尽量落在新文件、新 helper、新 exporter，而不是继续改胖 upstream 热点文件

---

## 5. 推荐执行顺序

## 主线 A：品牌与自动化基线（先做）

这条主线不变，仍按主方案继续执行。

### PR-A1：默认品牌替换

目标：

- 默认产品词收敛为 TokenKey
- 不改平台 identity、路由 identity、调度语义

验收：

- 默认 UI / 标题 / README / setting 不再以 `Sub2API` 为主产品词

### PR-A2：`newapi` 外显降级

目标：

- 在 admin UI 中把 `newapi` 默认展示为“扩展引擎 / Extension Engine”
- 底层 provider 术语只保留在高级信息层

验收：

- 不修改 `PlatformNewAPI`、`group.platform`、`channel_type` 等协议身份
- 只改展示层

### PR-A3：merge / bump 自动化基线

目标：

- `scripts/prepare-upstream-merge.sh`
- `scripts/bump-new-api.sh`
- weekly dry-run / compile-smoke CI
- brand drift check

验收：

- 上游 merge 与 new-api bump 不再依赖操作者记忆

---

## 主线 B：最小 Engine Spine（第二优先级）

这条主线是“极大减少和 upstream 冲突面”的核心抓手，因此在真正深入 traj 标准化之前，仍应优先推进。

### PR-B1：Engine Facade 引入

新增：

- `backend/internal/engine/facade.go`
- `backend/internal/engine/provider.go`
- `backend/internal/engine/dispatch_plan.go`

目标：

- 把最主要的 provider 选择和 dispatch 计划收口到 facade
- upstream 热点文件保留薄调用点

### PR-B2：关键 dispatch path 接入 facade

优先改造：

- `backend/internal/service/gateway_bridge_dispatch.go`
- `backend/internal/service/openai_gateway_bridge_dispatch.go`
- `backend/internal/service/openai_gateway_service.go`

要求：

- 只保留薄调用点
- TokenKey 特有判断进 companion / facade
- 不在热点文件里长出更多分支

### PR-B3：Capability truth table 收口

新增：

- `backend/internal/engine/capability.go`
- `backend/internal/engine/registry.go`

收口事实：

- compat 平台集合
- endpoint → provider 判定
- image / video / task fetch 与 `channel_type` 真值

### PR-B4：semantic sentinel 第一版

新增检查：

- 关键 dispatch path 必须走 facade
- compat truth source 不得回退为散点定义

---

## 主线 C：Evidence Spine（第三优先级，但拆成两段）

### 阶段 C-1：先完成生产级统一证据主链路

这一步只解决“系统默认证据面”，不强行一次满足 traj 标准全部字段。

#### PR-C1：Trajectory Record M1 正式化

建议新增：

- `backend/ent/schema/trajectoryrecord.go`
- `backend/internal/observability/trajectory/`

建议最小字段：

- `trajectory_id`
- `request_id`
- `user_id`
- `group_id`（如当前可稳定取得）
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

设计要求：

- 元数据进 PostgreSQL
- 大 payload 进 blob store / local blob
- blob 内容永远是脱敏后的 payload
- 不把 session 拼装逻辑塞进主写路径

#### PR-C2：fail-open 护栏闭环

补齐：

- async writer
- DLQ
- metrics
- capture completeness 统计
- terminal path hook check

验收：

- capture 失败不阻断主请求
- 失败一定留下 DLQ、计数与结构化日志
- 新增 endpoint 若未接 trajectory，preflight / CI 失败

### 阶段 C-2：从 evidence 派生符合 traj 标准的数据集

这一步才正式对齐 `traj 标准 v1.0`。

#### PR-C3：Session / Turn Projection

新增建议：

- `session_id`
- `turn_index`
- `role`
- `message_kind`
- `tool_name`
- `tool_schema_json`
- `tool_call_json`
- `tool_result_json`
- `model`
- `timestamp`

关键约束：

- `session_id` 不依赖特定 synth pipeline 才存在
- session 组装逻辑落在 projection/export 层，不侵入 gateway 热点文件
- 主写路径仍以请求级 evidence 为事实源

#### PR-C4：traj 标准导出器

新增：

- traj JSONL exporter
- session 拼接逻辑
- tool call / result 配对逻辑
- single-call 数据分组逻辑

导出目标：

- 满足 `traj 标准 v1.0` 所需字段
- role + content 可解析
- 工具调用具备 schema / call / result 三件套

#### PR-C5：traj 验收与门禁

新增脚本 / CI：

- H1：有效轮次 ≥ 2
- H2：结构化工具调用 ≥ 1
- H3：工具配对率 > 0.3
- D1：精确+子集去重率 < 20%

附加检查：

- JSONL / JSON 解析合法
- session 完整无截断
- tool 命名规范
- 无循环 / 无严重冗余

---

## 6. traj 标准补齐时的实现边界

为了继续降低 upstream 冲突面，traj 标准化必须遵守以下边界。

### 6.1 不在 upstream 热点 handler / service 中维护 session 状态

不要把以下文件继续改胖：

- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_account_scheduler.go`
- `backend/internal/service/gateway_bridge_dispatch.go`
- 大型 gateway handler 文件

session / turn / tool-use 的组装逻辑应进入：

- `backend/internal/observability/trajectory/*`
- 新 exporter / projection 文件
- companion helper

### 6.2 不改变现有协议身份来换取“数据更整齐”

为了 traj 数据集方便，不得：

- 重命名平台 identity
- 改 `group.platform` 语义
- 改 `channel_type` 事实源
- 改请求主路由的协议边界

### 6.3 不让“标准字段”直接驱动产品抽象膨胀

例如：

- `system prompt` 是数据集要求，不代表要立刻设计一个新的 prompt 平台产品层
- `session_id` 是组装主键，不代表要立刻做复杂 session runtime
- tool-use 三件套要抽取，但不代表要把全系统改造成 Agent runtime

数据集要求只补齐必要结构，不外溢成额外产品概念。

---

## 7. 需要新增的机械门禁

### 7.1 Evidence 主链路门禁

- `trajectory hook check`：新主流量 endpoint 未接入 trajectory 直接失败
- `terminal event check`：关键 terminal path 必须存在 finish capture
- `redaction version check`：敏感键集合变化必须同步 bump `redaction_version`

### 7.2 Engine Spine 门禁

- `engine facade hook check`：关键 dispatch path 必须通过 facade
- `compat truth-source check`：compat 平台真值不得重新散落到多个文件

### 7.3 traj 数据集门禁

- `session assembly check`
- `tool pairing check`
- `jsonl parse check`
- `dedupe threshold check`
- `single-call grouping check`

---

## 8. 建议的最近执行顺序

如果只给一个实际执行顺序，建议如下：

1. **PR-A1 默认品牌替换**
2. **PR-A2 `newapi` 外显降级**
3. **PR-A3 merge / bump 自动化基线**
4. **PR-B1 Engine Facade 引入**
5. **PR-B2 关键 dispatch path 接入 facade**
6. **PR-B3 Capability truth table 收口**
7. **PR-B4 semantic sentinel 第一版**
8. **PR-C1 Trajectory Record M1 正式化**
9. **PR-C2 fail-open 护栏闭环**
10. **PR-C3 Session / Turn Projection**
11. **PR-C4 traj 标准导出器**
12. **PR-C5 traj 验收与门禁**

这个顺序的理由是：

- A 先收紧产品心智与自动化基线
- B 先显著减少 upstream 合并税
- C 再把统一 evidence 做深，并在第二段对齐 traj 标准

这样既不偏离主方案，也不让 traj 标准把系统拉向更重的设计。

---

## 9. 成功标准

当以下条件同时满足时，可认为 PR #81 之后的下一阶段达标：

1. 默认产品词只剩 TokenKey，`newapi` 退到内部实现词或高级信息层。
2. 关键 dispatch path 的 TokenKey 特有判断大幅退出 upstream 热点文件。
3. 主路径请求都能查到统一的脱敏 evidence record。
4. capture 失败不会 silent-loss，而会留下 DLQ、指标与日志。
5. traj 导出不再只是 `qa_records.jsonl`，而是具备 `session_id / turn / tool-use` 结构化投影。
6. traj 数据集可通过 H1/H2/H3/D1 验收。
7. 新增 provider / endpoint / capture 点时，改动主要集中在 facade / registry / trajectory / exporter，而不是十几个 service 文件散改。

---

## 10. 最后一句话

PR #81 之后，正确的下一步不是“再堆一层更大的轨迹系统”，而是：

**先把品牌和自动化收紧，再把 Engine Spine 变薄，最后让请求级 evidence 成为唯一事实源，并从它稳定派生出符合 traj 标准的 session 数据。**
