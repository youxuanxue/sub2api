# TokenKey OPC 目标架构与演进准入标准

> 适用仓库：`tokenkey/sub2api`
>
> 目标：让 TokenKey 成为一个清晰、可运营、低合并税的 OPC 产品：对外只有 TokenKey；对内只有一个控制面、一条最小 Engine Spine、一条 Evidence Spine；每次 upstream merge 都继续缩小冲突面。

---

## 0. 结论

TokenKey 不能演化成“功能越来越多的大 fork”，也不能把当前代码现状包装成目标完成。

后续所有变更只看四件事：

1. **产品是否更简单**：用户只理解 TokenKey，不需要理解 sub2api、new-api、compat、bridge、projection 等内部词。
2. **运营是否更自动**：重复判断进入 owner、facade、sentinel、preflight 或 CI gate。
3. **运维是否更短路**：一次事故能从 request / trajectory correlation 进入 QA、tool、ops、stream terminal 证据。
4. **upstream 冲突面是否更小**：TokenKey-only 逻辑必须向 companion / facade / component 收敛。

上游更新非常频繁（例如 #81 级别 230+ commits、#110 级别 80+ commits）。最小 upstream 冲突面不是优化项，而是生存约束。

---

## 1. 不可逾越原则

### 1.1 一个产品

对外只有 TokenKey。

- 默认 UI、文档、支付说明、站点名使用 TokenKey 心智。
- `newapi` 是内部 platform / provider identity，不是第二个产品。
- Extension Engine / 扩展引擎可以作为展示词，但不得反向改协议身份。

不得改名：

- `group.platform = newapi`
- `PlatformNewAPI`
- `channel_type`
- bridge adaptor identity

### 1.2 一个控制面

TokenKey 控制面留在本仓库：用户、API key、分组、账号、调度、配额、支付、管理端、用户端。

不得为了接入 sibling `new-api` 能力而新建第二控制面，也不得在 `new-api` 仓库打 TokenKey 私有补丁。

### 1.3 最小 Engine Spine

Engine Spine 只回答：该走谁、能不能走、truth 在哪里。

当前 owner：

- `backend/internal/engine/facade.go`
- `backend/internal/engine/dispatch_plan.go`
- `backend/internal/engine/provider.go`
- `backend/internal/engine/capability.go`
- `backend/internal/engine/registry.go`

新增 endpoint / provider / capability 时，必须进入 Engine owner 或明确的 companion；不得在 gateway / service 热点文件新增平行 truth table。

Engine Spine 不负责吞并全部 scheduler、request/response transform、operator catalog 或 model catalog。保持最小，是为了减少判断点和 merge 冲突。

### 1.4 Evidence Spine：QA/tool 原始证据无感完整

Evidence 目标不是追外部数据集标准，也不是新增用户可见概念。

目标是：

- QA 请求与响应 100% 无感记录。
- tool 调用参数、返回结果、错误 100% 无感记录。
- stream chunk / terminal event 的关键事实 100% 无感记录。
- 记录形态保持原始交互语义；除 secret 脱敏外，任何归一化或投影都不得损失排障事实。
- 用户和多数 operator 不需要理解外部数据集标准、session、turn、projection 等内部词。

安全边界不变：raw secret 不进持久层，不进结构化日志；先脱敏，再持久化；capture fail-open，但不能 silent-loss。

### 1.5 最小 upstream 冲突面

热点 upstream 文件只保留薄调用点。TokenKey-only 逻辑必须进入 companion / facade / component。

高风险热点：

- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_account_scheduler.go`
- `backend/internal/service/gateway_bridge_dispatch.go`
- `backend/internal/server/routes/gateway.go`
- `frontend/src/views/admin/SettingsView.vue`

不得：

- silent-delete upstream feature。
- 为短期少改，把 provider/platform 分支留在热点文件。
- 复制已有 owner 的判断逻辑。
- 把“后续再抽”当默认处理。

### 1.6 Upstream 收敛边界

TokenKey 长期保留的 fork 差异只应服务核心产品能力：

- `newapi` 第五平台与 `channel_type`。
- bridge relay、OpenAI-compatible 分发、endpoint normalization。
- newapi admin 配置、上游模型元数据、affinity / selection。
- TokenKey 品牌、Engine Spine、Evidence Spine、发布与运维门禁。

外围能力默认收敛 upstream，不再维护平行私有支线：

- payment / webhook。
- Passkey / WebAuthn。
- Backend Mode。
- 只服务这些外围支线的 settings merge / audit / helper。

收敛不等于 silent-delete。任何删除 upstream-owned 能力前，必须先判断真实用户影响；能通过默认值、setting、companion short-circuit 解决时，不删除 upstream 能力。

---

## 2. 当前基线：可复用，但不是完成态

当前实现已经有可复用骨架：

- `qa_records` 是当前请求级 evidence metadata owner。
- `backend/internal/observability/qa/*` 捕获 request / response / SSE chunk。
- `backend/internal/observability/trajectory/*` 提供 writer / redaction / projection / exporter 实现边界。
- Engine owner 已承载 bridge endpoint、provider、capability、video channel support 等 truth。
- brand、engine、newapi、trajectory、terminal、redaction 等 sentinel / checker 已进入 preflight 或 CI。
- upstream merge PR 已按 `Merge Harness + Invariant Commit + OPC Refactor Commit` 组织。

这些只是准入下限，不代表目标完成：

- `qa_records` 存在，不等于 QA/tool 原始证据 100% 覆盖。
- Engine owner 存在，不等于热点文件已经收敛完成。
- sentinel 存在，不等于真实生产样本和排障闭环已经充分验证。
- UI 局部出现 TokenKey，不等于端到端产品心智已经统一。
- merge PR 通过 CI，不等于 upstream 冲突面已经下降。

---

## 3. 变更准入标准

任何 PR 必须满足：

1. 对外产品心智仍是 TokenKey。
2. 协议 identity 不为展示词让路。
3. 新 endpoint / provider / capability 有单一 owner。
4. 新 QA/tool 证据需求落在无感 capture / evidence owner / exporter 边界。
5. 新 capture payload 先脱敏再持久化；敏感字段变化必须同步 redaction version contract。
6. capture 失败可观测，不 silent-loss。
7. 热点文件只保留薄调用点。
8. 上游能力默认保留，不 silent-delete。
9. 高频 review 问题必须转成 checker / sentinel / fixture。
10. `./scripts/preflight.sh` 通过。

---

## 4. Upstream merge PR 标准

每个 upstream merge PR 分三类 commit：

1. **Merge Harness Commit**：保留 upstream 能力、解决冲突、编译、生成代码、接入已有 canonical hooks。
2. **Invariant Commit**：只修不可退让项：品牌回退、raw secret 泄漏、route canonical 破坏、QA/trajectory hook 缺失、redaction contract 漂移。
3. **OPC Refactor Commit**：收敛本次 merge 新增或显著增厚的 TokenKey 分叉面。

规则：

- 历史债务不得借 merge PR 扩张。
- 本次 merge 新增的热点分叉，必须同 PR 收敛。
- 确实不能收敛时，PR 必须写明阻塞原因，并补机械门禁防止继续扩张。

---

## 5. `qa_records` 与新表判断

当前不因命名新增同职责 `trajectory_records`。原因是 OPC 要减少事实源，不为表名洁癖增加双写、回填、retention、query、preflight 和 merge churn。

但 `qa_records` 不是神圣边界。出现以下情况，必须重新讨论 `trajectory_records`、`trajectory_events` 或其他事件模型：

- `qa_records` 无法表达 QA/tool 多事件 timeline。
- retention / query / export 无法通过索引或 projection 解决。
- QA/tool 调用与结果需要跨请求事实源。
- QA capture、tool 结果、stream terminal、ops error 需要统一到一条可查询 correlation 语义，而当前模型不足。
- 真实 fixture / 抽检证明当前 capture 无法达到 QA/tool 原始交互证据 100% 覆盖。

提出新模型必须同时给出 migration / backfill、cutover、rollback、focused tests、preflight 或 sentinel。

---

## 6. 当前未完成项

这些不是每个 PR 都要解决，但不能被说成已完成：

- QA/tool 调用与结果 100% 覆盖仍需更多真实 provider / endpoint / streaming fixture。
- tool 参数、返回、错误、stream terminal 的结构保真仍需测试证明。
- QA / ops / evidence 查询入口仍需更短的 correlation 路径。
- capture failure 的 DLQ、计数、日志、告警仍需 operator 可用闭环。
- `SettingsView.vue` 等热点 UI 仍需在被触碰时继续向 component / composable 收敛。
- token-presence sentinel 仍需逐步升级为 semantic call-site / fixture validator。

---

## 7. 成功标准

TokenKey 更接近 OPC 目标时，应持续满足：

1. 用户看到的是一个 TokenKey 产品。
2. operator 排障从一个 request / trajectory correlation 进入 QA、tool、ops、stream terminal 证据。
3. 新 endpoint / provider / capability 不再散落到 gateway / service 热点文件。
4. 上游合并时，TokenKey-only 分支越来越薄。
5. QA/tool 原始交互证据覆盖率由真实 fixture、semantic checker、capture completeness metrics 和抽检持续约束。
6. 高频 review 争议会变成机械门禁。
7. 当前实现如果无法满足目标，就升级实现；不得把目标降级成当前实现。

---

## 8. 一句话

**TokenKey OPC 的目标是：一个产品、一个控制面、一个最小 Engine Spine、一个无感完整记录 QA/tool 原始交互证据的 Evidence Spine，以及越来越小的 upstream 冲突面。当前实现只是载体，不能替代目标。**
