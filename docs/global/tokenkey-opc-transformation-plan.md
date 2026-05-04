# TokenKey 当前 OPC 架构基线与演进准入标准

> 适用仓库：`tokenkey/sub2api`
>
> 当前定位：这不是待执行的大重构计划，而是以现有代码为事实来源的 OPC 架构基线、上游合并准入标准与后续演进边界。
>
> 核心目标：对外只有 TokenKey，一个控制面、一条最小 Engine Spine、一个请求级 Evidence Spine；对内用机械门禁持续减少 `sub2api upstream` 与 sibling `new-api` 双上游带来的长期合并税。

---

## 0. 先给结论

当前 TokenKey 已经不是“等待开始 OPC 改造”的状态。本仓库在 upstream 合并迭代（含 PR #110）下，已具备一组可执行的 OPC 基线（对照 §1.3：基线是准入下限与演进起点，不是长期目标的完成声明）：

1. **产品基线**：对外默认心智收敛为 TokenKey；`newapi` 保留为内部 identity，不作为第二套产品心智外显。
2. **控制面基线**：用户、分组、账号、配额、支付、后台、网关入口继续以本仓库为唯一控制面；不新建第二控制面 repo。
3. **Engine Spine 基线**：桥接能力与 endpoint/provider 判定已有 `backend/internal/engine/` 作为 owner；热点 service 文件只能保留薄调用点。
4. **Evidence Spine 基线**：请求级证据事实源是现有 `qa_records`，不是另起一个 `trajectory_records`。`qa_records` 已承载 `trajectory_id`、脱敏 blob URI、redaction version、capture status 与导出所需元数据。
5. **Merge Doctrine 基线**：上游合并不再只是冲突解决；每个 merge PR 都必须按 `Merge Harness + Invariant Commit + OPC Refactor Commit` 收敛本次合并带来的新增分叉面。
6. **机械门禁基线**：preflight、sentinel、PR shape、frontend dist、traj dataset validator 等已经是合并准入的一部分；不接受“靠文档记住”的软规则。

因此，后续判断不再问“这个方案未来要不要做”，而是问：

> **这次变更是否保持了现有 owner，是否减少了热点分叉，是否让证据更靠近唯一事实源，是否有机械门禁防止回退。**

---

## 1. 文档职责与事实来源

### 1.1 文档职责

本文只承担四件事：

- 描述当前已经成立的 OPC 架构基线。
- 明确不能被当前实现稀释的产品与运维目标。
- 定义后续变更必须遵守的准入标准。
- 列出仍未完成的演进缺口，并明确哪些缺口不应在普通 upstream merge PR 中扩张。

本文不再维护一份脱离代码状态的 PR-A/B/C 大路线图。路线图如果不能被代码 owner、测试或 preflight gate 支撑，就不是 OPC 基线，只能算设计草稿。

### 1.2 事实来源优先级

当文档与代码冲突时，以以下顺序判定事实：

1. 生产代码与 Ent schema。
2. `scripts/preflight.sh` 与其调用的 checker / sentinel registry。
3. CI workflow，尤其 upstream merge PR shape 与 weekly automation smoke。
4. 本文档。

文档不能把“建议新增”写成已经存在，也不能把已经实现的 owner 写回未来计划。例如：当前请求级 Evidence Spine 的 schema owner 是 `qa_records`，因此本文不再把 `trajectory_records` 写成必须新增的默认目标。

### 1.3 不可降级目标

代码事实只能说明“现在系统怎样工作”，不能自动变成“架构目标已经完成”。TokenKey 后续演进必须同时服务三类不可降级目标：

- **乔布斯产品目标**：用户只理解一个 TokenKey 产品；内部 provider、bridge、compat、newapi 等实现细节不能泄漏成第二套产品心智。
- **OPC 运营目标**：重复判断必须被收敛成 owner、facade、sentinel、preflight 或 CI gate；事故排查路径必须越来越短，而不是靠更多人工 checklist。
- **最小 upstream 冲突面目标**：每次 upstream merge 后，热点文件中的 TokenKey-only 逻辑应该更薄、更靠近 companion / facade / component，而不是被“代码事实”合理化为长期形态。

因此，本文说“当前已实现基线”时，只表示它是继续演进的起点和准入下限，不表示完整目标已经达成。

---

## 2. 不变原则

### 2.1 一个产品

对外默认只有 TokenKey：

- UI、README、支付说明、默认站点名等 outward surfaces 使用 TokenKey 心智。
- `newapi` 是内部 provider / platform identity，不是默认展示给用户的第二产品名。
- “Extension Engine / 扩展引擎”是 UI 展示词；不得反向重命名协议 identity。

禁止为了品牌统一改动：

- `group.platform = newapi`
- `PlatformNewAPI`
- `channel_type`
- bridge adaptor identity

### 2.2 一个控制面

TokenKey 控制面继续留在本仓库：

- 用户体系
- API Key / JWT / 权限
- 分组与账号
- 调度与配额
- 支付与运营
- 管理端与用户端 UI

不得为了接入 sibling `new-api` 能力而新建第二控制面或在 `new-api` 仓库中打 TokenKey 私有补丁。

### 2.3 一个最小 Engine Spine

Engine Spine 的职责是回答“该走谁、能不能走、truth 在哪里”，不是吞并所有 gateway/service 逻辑。

当前代码 owner：

- `backend/internal/engine/facade.go`
- `backend/internal/engine/dispatch_plan.go`
- `backend/internal/engine/provider.go`
- `backend/internal/engine/capability.go`
- `backend/internal/engine/registry.go`

当前已集中在 Engine Spine 的事实：

- bridge endpoint 是否启用。
- endpoint 的 provider。
- endpoint 支持的 scheduling platform。
- endpoint 是否要求 `channel_type`。
- video submit/fetch 是否要求 task adaptor。
- video channel type 是否由 upstream task adaptor registry 支持。

当前明确不由 Engine Spine 承担的内容：

- 全部账号调度算法。
- 全部 request/response transform。
- 完整 operator catalog。
- 完整 model pricing/catalog 展示层。

### 2.4 一个请求级 Evidence Spine

当前请求级 Evidence Spine 的事实源是 `qa_records`。这句话的边界很重要：`qa_records` 足以承担请求级 evidence metadata，不等于 session / turn / tool-use / ops correlation 的完整 trajectory 目标已经完成。

`backend/ent/schema/qa_record.go` 已经包含请求证据需要的核心字段：

- `request_id`
- `trajectory_id`
- `user_id`
- `group_id`
- `api_key_id`
- `account_id`
- `platform`
- `provider`
- `channel_type`
- `requested_model`
- `upstream_model`
- `inbound_endpoint`
- `upstream_endpoint`
- `status_code`
- `success`
- `duration_ms`
- `first_token_ms`
- `stream`
- token usage
- `request_sha256`
- `response_sha256`
- `blob_uri`
- `request_blob_uri`
- `response_blob_uri`
- `stream_blob_uri`
- `redaction_version`
- `capture_status`
- `tags`
- synthetic pipeline tagging fields
- `retention_until`

因此，本文不再把请求级 `trajectory_records` 作为当前必须新增的 schema。只有当 `qa_records` 无法承载查询、retention、事件粒度、ops 关联或跨请求 session 事实源需求时，才允许提出新表；提出新表必须包含迁移计划、双写/回填策略、回滚策略和 preflight gate，不能只因为名字更准确而新增。

### 2.5 先脱敏，再持久化

Evidence Spine 的硬边界：

- raw secret 不进入持久层。
- raw secret 不进入结构化日志。
- blob 内容必须是脱敏后的 evidence。
- redaction policy 变化必须同步更新版本契约。
- capture 写入 fail-open，但不能 silent-loss；失败必须进入 DLQ、计数器或结构化错误日志。

---

## 3. 当前已实现基线

### 3.1 Upstream Merge Doctrine

当前已实现：

- `scripts/prepare-upstream-merge.sh`：上游合并准备脚本。
- `.github/workflows/upstream-merge-pr-shape.yml`：merge PR shape gate。
- `scripts/check-upstream-drift.sh` 与 `.github/workflows/upstream-drift-monitor.yml`：上游漂移监测。
- PR #110 已按 merge rehearsal 执行并通过 CI。

后续 upstream merge PR 必须分清三类 commit。

#### Merge Harness Commit

只负责：

- 保留 upstream 能力与审计链。
- 解决冲突、编译、生成代码、基础测试。
- 把新增高风险入口先接入现有 canonical hooks。
- 保证 preflight / sentinel / PR shape gate 能运行。

#### Invariant Commit

只修不可退让项：

- 品牌回退。
- raw secret 泄漏。
- route canonical 破坏。
- trajectory / QA capture hook 缺失。
- redaction version contract 漂移。
- `AGENTS.md` / contract docs 可追踪性回退。

#### OPC Refactor Commit

只收敛本次 merge 引入或触碰后显著增厚的分叉面：

- 热点文件新增 TokenKey 分支迁回 companion / facade / owner。
- 平行 truth table 收敛到单一 owner。
- 大型 UI 文件新增策略块抽为 component / composable。
- 新增 owner 必须有 test 或 semantic sentinel。

历史债务不得借 upstream merge PR 扩张。

### 3.2 Engine Spine

当前已实现：

- `BuildDispatchPlan` 通过 `CapabilityForEndpoint` 输出 bridge/native plan。
- `CapabilityForEndpoint` 是 bridge endpoint capability truth 的 owner。
- `BridgeEndpointEnabled` 不允许在 engine 外复制。
- video channel support 使用 `engine.IsVideoSupportedChannelType`，不允许外部调用 bridge-local truth。
- direct `bridge.Dispatch*` 调用被限制在 approved service boundary files。
- `scripts/engine-facade-sentinels.json` 与 `scripts/check-engine-facade-hooks.py` 保护关键路径。
- `scripts/preflight.sh` 额外检查 engine dispatch / capability drift。

当前未承诺：

- Engine Spine 不负责重写所有 provider service。
- Engine Spine 不负责对外 catalog。
- Engine Spine 不负责替代 Ent、repository、setting service。

### 3.3 OpenAI Upstream Capability Truth

当前已实现：

- `backend/internal/pkg/openai_compat/upstream_capability.go` 是 OpenAI-compatible upstream capability 的 owner。
- `ShouldUseResponsesAPI` 决定 OpenAI APIKey 是否走 Responses。
- `ResponsesEndpointSupportedByStatus` 决定探测状态码语义。
- `backend/internal/service/openai_apikey_responses_probe.go` 调用 `openai_compat` owner，不再本地维护状态码 truth。
- `scripts/preflight.sh` 禁止 `isResponsesEndpointSupportedByStatus` 重新散落到 service 文件。

准入标准：

- 新增 OpenAI-compatible upstream capability 判定时，优先进入 `openai_compat` 或 Engine owner。
- 不允许在 gateway/service 热点文件中新增独立 truth table。

### 3.4 New API 作为内部能力来源

当前已实现：

- `.new-api-ref` 是 sibling `new-api` pin 的事实源。
- `scripts/sync-new-api.sh` 负责 sync / check / bump。
- `scripts/bump-new-api.sh` 是 bump wrapper。
- `backend/internal/integration/newapi/*` 与 `backend/internal/relay/bridge/*` 是集成边界。
- `scripts/newapi-sentinels.json` 与 `scripts/check-newapi-sentinels.py` 保护 load-bearing surfaces。
- scheduler/gateway filters 必须使用 `IsOpenAICompatPoolMember` / `OpenAICompatPlatforms`，preflight 阻止 bare `PlatformOpenAI` 回退。

准入标准：

- 上游新增 adaptor 能力时，先判断是否进入 Engine capability owner。
- 不在 sibling `new-api` 中打 TokenKey 私有补丁。
- 不把 `newapi` 恢复成默认外部产品词。

### 3.5 Brand / Product Surface

当前已实现：

- outward TokenKey brand surfaces 有 `scripts/brand-sentinels.json` 与 checker 保护。
- 管理端平台展示已区分内部 key 与展示词；新增策略块（如 OpenAI Fast/Flex）已抽到 dedicated component，避免热点 view 继续堆叠。
- 支付文档在「从 Sub2ApiPay 迁移」等段落仍引用第三方项目历史名，属于迁移指称而非 TokenKey 主产品心智；新增 outward 文案须同步纳入 brand sentinel。

准入标准：

- 展示层可以使用 TokenKey / Extension Engine。
- 协议层 identity 不因展示词变化而改名。
- 新 outward surface 如果承载产品心智，必须同步补 brand sentinel。

### 3.6 Request-level Evidence Spine

当前已实现：

- `qa_records` 是请求级 evidence metadata owner。
- `backend/internal/observability/qa/service.go` 捕获 request/response/SSE chunk，异步写入，队列满时同步 fallback。
- `backend/internal/observability/trajectory/writer.go` 支持 blob store 写入与 DLQ fallback。
- `backend/internal/observability/qa/sse_tee.go` 负责 response/SSE tee。
- `backend/internal/observability/qa/service_traj_export.go` 从 `qa_records` 与 blob 派生 `trajectory.jsonl` export。
- `backend/internal/observability/trajectory/projection.go` 从 request-level evidence 派生 session/turn/tool-use 行。
- `scripts/trajectory-sentinels.json` 与 checker 保护 route hook。
- `scripts/terminal-sentinels.json` 与 checker 保护 stream terminal semantics。
- redaction version contract 有 `scripts/redaction-sentinels.json` 与 checker。

准入标准：

- 新主流量 endpoint 必须接入 `trajectory_id` 与 QA capture。
- 新 capture payload 必须先脱敏再持久化。
- 敏感字段集合变化必须同步更新 redaction version contract。
- capture 失败必须可观测，不能 silent-loss。

---

## 4. `qa_records` 与 `trajectory_records` 的结论

### 4.1 当前结论

`qa_records` 已经基本承担了文档旧版设想中“请求级 `trajectory_records`”的 evidence metadata 职责。当前不应再把同职责的新表写成必做项。

更准确的表达是：

> `qa_records` 是当前请求级 Evidence Spine 的事实源；`trajectory` package 是 projection / writer / redaction helper；`trajectory.jsonl` 是从 `qa_records` + blob 派生出的数据集视图。完整 trajectory 目标仍然包含 session grouping、turn 结构、tool-use 配对、ops error correlation 与验收闭环，不能因为请求级表已经存在就宣告完成。

### 4.2 为什么不立刻新增 `trajectory_records`

新增 `trajectory_records` 会带来新的复杂度：

- 与 `qa_records` 双写或迁移。
- 历史数据回填。
- retention 与 export query 双 owner。
- preflight / tests / docs 全部新增漂移面。
- 上游 merge PR 中产生非必要 schema churn。

这不符合当前 OPC 目标。当前真正的缺口不是“表名不够准确”，而是 session 级 projection、ops 关联、tool-use 配对和验收闭环还需要继续加强。

### 4.3 什么时候才允许提出新表

只有出现以下至少一种情况，才允许重新讨论 `trajectory_records` 或 `trajectory_events`：

- `qa_records` 的字段语义无法表达多事件 timeline。
- retention / query / export 性能无法通过索引或 projection 解决。
- 需要跨请求 session 级事实源，而不是导出时 projection。
- 需要把 QA product surface 与 Evidence Spine storage 明确拆库/拆权限。
- 需要把 `ops_error_logs`、QA capture、traj export 统一到同一条可查询的 evidence correlation 语义，而 request-level projection 已经不足。

提出时必须同时给出：

- schema diff。
- migration / backfill。
- 双写或 cutover 策略。
- 回滚策略。
- focused tests。
- preflight or sentinel。

---

## 5. Session 级 traj Projection：当前状态与缺口

### 5.1 当前已具备

当前 `trajectory.ProjectRecords` 已能从 `qa_records` + evidence blob 派生：

- `session_id`
- `turn_index`
- `role`
- `message_kind`
- `tool_name`
- `tool_call_id`
- `tool_schema_json`
- `tool_call_json`
- `tool_result_json`
- `content_json`
- `model`
- `timestamp`
- `request_id`
- `trajectory_id`

`ExportUserTrajectoryData` 已能导出 `trajectory.jsonl` zip。

### 5.2 当前仍未完全成立

以下不应被文档误写成已完全完成：

- 默认 session 语义仍主要由 `synth_session_id`、`trajectory_id`、`request_id` fallback 派生，不是全局一等 session runtime。
- turn 划分是 projection 逻辑，不是生产主路径持久化实体。
- tool schema / call / result 是 best-effort extraction，不等于所有 provider 都有完整结构化配对。
- H1/H2/H3/D1 validator 已有回归测试与 preflight 入口，但真实生产导出质量仍依赖采样与数据覆盖。

### 5.3 后续演进边界

后续要增强 traj projection，应优先做：

- projection extraction tests 覆盖更多 provider payload。
- dataset validator 对真实 export fixture 的回归样本。
- session grouping policy 的显式 contract。
- tool pairing summary metrics。

不要做：

- 把 gateway 主路径改成 session runtime。
- 为了数据集字段新增一个 Agent 产品层。
- 为了命名洁癖新增 `trajectory_records` 双表。

---

## 6. Upstream Merge 准入矩阵

| upstream 带来的变化 | 必须保持的 owner | PR 内动作 | 机械门禁 |
|---|---|---|---|
| 新主流量 endpoint | route + QA capture + trajectory hook | 接入现有 hook；必要时补 route helper | trajectory / terminal sentinel |
| 新 bridge endpoint | `backend/internal/engine/capability.go` | 增加 capability；热点文件只调用 facade | engine facade / capability preflight |
| 新 OpenAI upstream capability | `internal/pkg/openai_compat` 或 `engine` | 收敛到 owner；补 focused test | OpenAI capability truth check |
| 新 `newapi` load-bearing surface | `internal/integration/newapi` / bridge boundary | 保留 upstream 能力；补 sentinel | newapi sentinel registry |
| 新 product-facing 文案 | brand/i18n owner | 展示 TokenKey/Extension Engine；不改协议 identity | brand sentinel |
| 新 sensitive payload | `logredact` + QA redaction version | 先脱敏再持久化；bump contract | redaction version check |
| 新 admin 策略 UI | dedicated component/composable | 主 view 只保留 wiring | frontend lint/typecheck/tests |

---

## 7. 热点文件规则

以下文件是 merge-tax 高风险区：

- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_account_scheduler.go`
- `backend/internal/service/gateway_bridge_dispatch.go`
- `backend/internal/server/routes/gateway.go`
- `frontend/src/views/admin/SettingsView.vue`

允许：

- 薄调用点。
- upstream 语义保真 conflict resolution。
- DTO 字段拼接。
- 调用 companion / facade / component。

“薄调用点”的判定标准：

- 只做参数组装、owner 调用、错误返回和少量上下文绑定。
- 不新增本地 truth table。
- 不新增 provider/platform 分支树。
- 不复制已有 owner 的判断逻辑。
- 如果新增 TokenKey-only 分支超过一个局部判断，必须说明为什么不能进入 companion / facade / component，并优先补 semantic sentinel。

不允许：

- 新增 TokenKey-only truth table。
- 新增 provider-specific 大分支。
- 重复复制 capability / route / platform predicate。
- 在大型 Vue view 中继续堆策略 UI 细节。

如果 upstream merge 触碰这些文件并导致 TokenKey 分叉增厚，必须在同一个 PR 里用 OPC Refactor Commit 收敛本次新增分叉。

最小 upstream 冲突面不是“少改代码”，而是让长期冲突集中在少数薄注入点。为了短期少改而把新分支留在热点文件，属于把当前代码事实误当成目标。

---

## 8. 当前未完成项

这些是明确未完成或未完全完成的演进方向；它们不是 PR #110 的遗留 bug，也不应自动塞进每个 upstream merge PR。

### 8.1 Evidence / traj

- session grouping policy 需要更明确的 contract，不能长期依赖 `synth_session_id` / `trajectory_id` / `request_id` fallback 的隐式优先级。
- tool-use extraction 需要更多真实 provider fixture，并产出 pairing summary metrics。
- H1/H2/H3/D1 需要更多真实 export 样本覆盖，不能只靠结构 validator 证明数据质量。
- QA / ops / evidence 查询入口仍需统一到同一条 correlation 语义；`ops_error_logs.trajectory_id` 与 `qa_records.trajectory_id` 的关联应成为排障路径，而不是两个相邻字段。

### 8.2 Engine

- Engine Spine 已有最小 owner，但还不是完整 provider catalog。
- 部分 provider 细节仍留在 service/bridge boundary，这是当前接受的边界。
- 如果新增 endpoint/provider，应优先补 capability owner 与 sentinel，而不是建设完整 catalog。

### 8.3 Frontend

- `SettingsView.vue` 已对 PR #110 新增 Fast/Flex 策略抽组件，但整页仍很大。
- 后续只在被新变更触碰且增厚时继续抽 dedicated component/composable。
- 不做为了“看起来更干净”的一次性大拆分。

### 8.4 Automation

- 现有 preflight 已覆盖核心漂移，但若 review 中反复发现同类人工遗漏，必须转成 checker。
- 不新增只报告不阻断的软 gate。

---

## 9. 成功标准

成功标准分两层：单次变更准入，和长期架构结果。前者防止回退，后者防止把当前最小实现包装成完成态。

### 9.1 单次变更准入

一项变更符合当前 OPC 基线，需要同时满足：

1. 对外产品心智仍是 TokenKey。
2. 协议 identity 没有为展示词让路。
3. 新 route / endpoint / provider 能找到单一 owner。
4. 新请求证据仍落到 `qa_records` + 脱敏 blob，不新增平行事实源。
5. 新 session/traj 需求优先落在 projection/export 层；若必须进入持久层，需要说明为什么 `qa_records` + projection 不足。
6. 热点文件只保留薄调用点。
7. 上游能力被保留，不 silent-delete。
8. 关键不变量有 test、preflight 或 sentinel。
9. `./scripts/preflight.sh` 通过。

### 9.2 长期架构结果

当以下结果持续成立时，才能说 TokenKey 更接近乔布斯 / OPC 风格的可持续产品骨架：

1. 用户看到的是一个 TokenKey 产品，而不是 sub2api、new-api、compat、bridge 的组合说明书。
2. 运维排障可以从一个 request / trajectory correlation 进入 QA capture、ops error、stream terminal 与导出数据，而不是在多个日志面手动拼接。
3. 上游合并时，热点文件里的 TokenKey-only 分支越来越薄，冲突更集中在 companion / facade / component 的边界。
4. Engine Spine 继续保持最小，但新增 endpoint/provider capability 不再散落到 gateway/service 文件。
5. Evidence Spine 继续以 `qa_records` 为请求级事实源，但 session/turn/tool-use/ops correlation 的质量由 projection tests、真实 fixture 和 validator 持续约束。
6. 高频 review 争议会被转成 checker，而不是沉淀成“以后记得”的人工规则。

---

## 10. 一句话

**当前 TokenKey OPC 的事实基线是：控制面在本仓库，Engine truth 进 `backend/internal/engine`，请求级 evidence truth 进 `qa_records`，traj 数据集从 evidence 派生；但完整目标仍是更简单的产品心智、更短的运维排障路径和更小的 upstream 冲突面，每次 upstream merge 都必须让本次新增分叉收敛，而不是把当前实现包装成完成态。**
