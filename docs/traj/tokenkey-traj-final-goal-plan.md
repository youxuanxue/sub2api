# TokenKey × traj 最终目标达成计划

## Context

最终目标不是做一套“专门给 traj 的旁路系统”，而是让 **真实用户调用** 与 **traj 合成用户/助手回合调度调用** 尽可能走同一条 TokenKey 产品路径：同一套网关、同一套鉴权、同一套路由、同一套调度、同一套 QA capture、同一套导出与清理机制。差异只保留在最小必要处：合成流量需要可识别、可隔离、可清理、可追溯。

这更符合乔布斯式产品哲学：好的系统不是堆功能，而是把复杂性藏在一个清晰、端到端一致的体验背后。也符合 OPC 哲学：一个人维护 N 个 Agent 的系统，必须压缩分叉路径，减少特殊逻辑，减少长期运维面，让真实生产路径天然成为合成数据路径的验证面。

因此，本计划的主线调整为：

1. **TokenKey 不为 traj 发明第二套调用链路。** traj 的 user/assistant 调用尽量模拟真实用户经过 TokenKey 的标准路径。
2. **traj 维护 transcript 真相，但不是为了替代 TokenKey evidence。** 它维护的是“对话意图与回合编排真相”，TokenKey 维护的是“生产网关观测证据”。
3. **TokenKey 只做采集、导出、基础结构性校验和极低成本清理。** 深度质量分级、清洗、分档、提升，放到 traj 更合理。
4. **线上存储成本必须接近 0。** 从 AWS prod 容器导出到本地成功后，自动清理过程文件、线上 DB 记录、S3/对象存储临时文件。
5. **assistant 侧优先走 Claude Code headless 非交互模式。** `claude -p` 可通过 session persistence、`--resume`、`--continue` 支持多轮上下文；traj 需要显式管理每条 synthetic session 对应的 Claude session id。

## Product principles

### 1. 真实调用与合成调用尽量同构

真实用户调用和 traj 合成调用的差异越大，越容易产生两类坏结果：

- 真实生产问题无法被合成数据覆盖；
- 合成数据看似质量高，但训练/评测时学到的是一条旁路系统的行为。

乔布斯视角下，产品体验应该是一条“自然路径”，而不是工程师为了内部方便搭出来的多条岔路。OPC 视角下，每多一条特殊链路，就多一倍未来合并、排障、校验、文档和心智负担。

因此，traj 合成 user/assistant 调用应尽量复用真实调用路径：

- 走 TokenKey 标准公网/内网网关入口；
- 走标准 API key/JWT 鉴权；
- 走标准 group/account/platform 调度；
- 走标准 upstream relay 与响应解析；
- 走标准 QA capture 与 evidence blob；
- 走标准 self/prod export 流程。

允许的差异只包括：

- `X-Synth-*` 元数据头，用于识别合成流量；
- 专用 API key / group / account 池，用于成本隔离和模型隔离；
- 专用 `trajectory_id` / `synth_session_id`，用于回合归并；
- 导出成功后的线上清理策略，用于成本最小化；
- assistant 侧 Claude Code headless session 管理，用于复现真实 coding agent 的多轮行为。

### 2. 最小 upstream 冲突面

TokenKey 是 upstream fork，所有改动都要避免把 traj 专用逻辑灌进 upstream-owned 大文件。实现原则：

- 优先新增 `*_tk_*.go` companion 文件；
- upstream 文件只保留薄注入点，例如一行 middleware、一个 helper 调用、一个 route registration；
- 不删除 upstream 功能；
- 不把 traj 的调度、分级、清洗逻辑放进 TokenKey；
- TokenKey 侧只沉淀“生产网关也需要”的通用能力：correlation、capture、export、purge、contract check。

## Why traj owns transcript truth

### 价值判断

traj 维护 transcript 真相的必要性，不在于“TokenKey 不能导出消息”，而在于两者记录的是不同层面的真相：

- **traj transcript**：谁在第几轮说了什么、为什么进入下一轮、何时终止、assistant 工具调用如何被编排。这是“对话产品真相”。
- **TokenKey QA evidence**：真实 HTTP 请求/响应、流式片段、header、模型、token、状态码、错误、上游响应。这是“生产观测真相”。

如果只依赖 TokenKey evidence 反推 transcript，会出现几个问题：

1. 网关看到的是协议层请求/响应，不一定知道调度器为什么进入下一轮。
2. Claude Code/agent 的本地工具执行、session resume、用户模拟器决策，很多发生在网关之外。
3. 多 provider 协议形态不同，从 evidence 反推统一 turns 容易变成一堆启发式和特殊分支。
4. 训练数据需要的是干净的回合意图结构；审计需要的是原始证据。两者混在一起会损害两边质量。

### 乔布斯视角

乔布斯式设计强调“端到端体验的主线”。traj transcript 就是合成对话产品的主线：从需求、persona、用户追问、assistant 执行、工具使用、验收到终止，形成一条人能理解、机器能复现的故事线。

如果让 TokenKey evidence 反向拼这个故事线，等于让底层日志系统决定产品叙事。这不是简洁，而是把产品体验交给偶然的实现细节。

### OPC 视角

OPC 要求系统可自动化、可审计、可低人力维护。traj transcript 作为真相有三点杠杆价值：

1. **可重放**：调度器可以基于 transcript 明确恢复 session，而不是猜测网关日志。
2. **可分层验证**：traj 验证“对话是否像一个高质量工程过程”，TokenKey 验证“生产路径是否真实捕获”。
3. **可替换底层**：未来 assistant 从 Messages API shim 切到 Claude Code `-p`，或 user 从 API shim 切到 Cursor Agent CLI，transcript contract 不需要跟着生产 evidence 改。

结论：traj 必须维护 transcript 真相，但它不应该复制 TokenKey 的生产 evidence。两者互补，不互相替代。

## What TokenKey should validate

### TokenKey 质量校验的边界

TokenKey 做质量校验的价值，不是给 traj 数据打分、分档、清洗、提升；这些更适合 traj。TokenKey 只应做“生产网关 substrate 必须保证”的低层结构性校验：

- route/capture hook 没有漂移；
- `trajectory_id` / `request_id` / `synth_session_id` 可关联；
- export JSONL 可解析；
- session/turn 基本连续；
- tool call/result 基本配对；
- 必填字段存在；
- 导出文件完整且可下载；
- purge 后线上过程数据确实被清掉。

这些检查的意义是证明 TokenKey 这条生产链路“可被信任”。它不负责判断一次合成对话是不是 P7 水平、是不是高训练价值、是否需要清洗重写。

### 为什么深度质量应放到 traj

深度质量判断依赖合成目标和训练策略，例如：

- persona 是否稳定；
- 用户是否像真实工程 owner；
- assistant 是否有效使用工具；
- 是否出现无效循环；
- 需求是否被收敛；
- 回合是否有足够工程密度；
- 数据应该进入 bronze/silver/gold 哪一档；
- 是否需要清洗、裁剪、增强、拒收。

这些是 traj 的产品域，不是网关域。放在 TokenKey 会带来三类问题：

1. **upstream 冲突面扩大**：网关代码被迫理解合成数据质量。
2. **职责污染**：TokenKey 从 API gateway 变成 dataset judge。
3. **成本上升**：线上服务承载离线分析逻辑，不符合极简生产路径。

### 乔布斯与 OPC 结论

乔布斯式产品会把“体验判断”放在产品层，而不是基础设施层。OPC 会把重计算、重清洗、重分级放到离线可替换模块，而不是放进线上 gateway。

所以最终分工应为：

- **TokenKey**：证明生产调用链路真实、完整、可导出、可清理。
- **traj**：判断数据是否好、属于哪一档、是否要清洗提升、是否进入训练集。

## Prod export and purge philosophy

TokenKey 的导出链路应体现“极致减少在线存储成本”：线上只保留生成数据所需的短生命周期过程状态；一旦离线导出成功，线上应自动清理。

推荐复用当前仓库已有链路，不新增 traj-only 导出脚本：

```text
traj run
  → calls TokenKey prod gateway with synth metadata
  → TokenKey captures QA/evidence in prod DB + qa_blobs
  → scripts/fetch-prod-qa-dump.sh exports qa_records + qa_blobs to ./.dump_trajs/
  → local manifest/count/checksum validation passes
  → scripts/prod-qa-export-and-purge.sh purges prod QA buffer:
       - TRUNCATE qa_records
       - remove /var/lib/tokenkey/app/qa_blobs contents
       - remove /var/lib/tokenkey/app/qa_dlq contents if present
       - delete S3 staging tarball
       - optionally delete local tarball after extract
  → traj consumes local artifact for grading/cleaning/training
```

当前脚本的语义是 **prod QA buffer 全量导出 + 全量清理**，不是按单个 `synth_session_id` 精确删除。短期最小冲突路径是复用它：在专用 synth 采集窗口或低峰窗口运行，接受“QA buffer 是短生命周期缓存”的产品语义。若未来必须保留真实用户 QA 数据更久，再在 companion 脚本中新增按 manifest/request id/blob uri 精确清理，不能把复杂清洗逻辑塞进 TokenKey 服务主路径。

### 当前仓库已有资产

| 文件 | 当前用途 | 结论 |
|---|---|---|
| `scripts/fetch-prod-qa-dump.sh` | 通过 SSM 在 Stage-0 EC2 打包 `qa_records` 与 `qa_blobs`，经 S3 presigned PUT 暂存后下载到本地 `./.dump_trajs/`，生成 `.last-prod-qa-export.json` manifest | **保留并复用**；已作为只导出入口 |
| `scripts/prod-qa-export-and-purge.sh` | 调用 `fetch-prod-qa-dump.sh`，本地校验后执行 prod `qa_records`/`qa_blobs`/`qa_dlq`/S3 staging 清理 | **保留并复用**；作为低成本 export+purge 主入口 |
| `deploy/aws/README.md` | operator runbook，已有 “Prod QA 全量导出与清理” 章节 | **保留并同步**；作为人工操作权威说明 |
| `scripts/check-traj-dataset.py` | 对导出 trajectory dataset 做 H1/H2/H3/D1 与结构性检查 | **保留**；定位为 substrate structural gate，不做深度质量评分 |
| `scripts/check-trajectory-hooks.py` + `scripts/trajectory-sentinels.json` | 防止 gateway trajectory/QA capture hook 漂移 | **保留**；后续可演进为更语义化检查 |
| `backend/internal/observability/qa/service_traj_export.go` | 用户自助 trajectory zip 导出服务层 | **保留**；用于 self-service/API 导出，不替代 prod QA dump 链路 |
| `exports/cursor-transcripts-*` | 历史导出结果/报告，不是自动化脚本 | **不作为当前链路复用**；不在本计划中继续引用 |

本轮清理原则：不删除仍有审计价值的历史数据/approved 文档，不新增第二套脚本；只把计划文档和 operator README 收敛到上述现有主链路。真正过时的是“需要新建 prod export + purge 脚本”的表述，因为仓库已经有可复用脚本。

设计要点：

1. **导出成功之前不清理。** 必须先完成本地落盘、manifest、计数与 checksum 校验。
2. **当前清理粒度是全量 QA buffer。** 运行窗口应选择低峰或专用 synth 采集窗口；如需精确 scope purge，后续只能在脚本层按 manifest 增量演进。
3. **线上不做长期数据湖。** TokenKey prod 只承担短期 capture buffer，不承担训练数据仓库。
4. **失败可重试。** 导出失败、manifest 缺失、checksum 失败、本地文件不完整时不清理线上数据。
5. **成功后默认清理。** 这符合 OPC 的成本纪律，减少 DB/S3/EBS 长期膨胀和隐私/合规暴露面。

这比在 TokenKey 长期保存 traj 数据更符合乔布斯和 OPC：产品路径清晰、运营成本低、失败模式少、长期心智负担小。

## Claude Code headless assistant adapter

assistant 侧应尽量使用 Claude Code 的真实 headless 能力，而不是长期停留在 Messages API shim。

### 能力判断

`claude -p` 非交互模式支持多轮上下文，但需要显式管理 session：

- `claude -p` / `--print`：非交互输出；
- `--output-format json`：便于捕获 `session_id`；
- `--resume <session-id>` / `-r`：恢复指定 session；
- `--continue` / `-c`：继续当前目录最近 session；
- `--fork-session`：从已有 session 分叉；
- 默认 session persistence 开启；如使用 `--no-session-persistence` 则不能依赖本地 resume。

### 推荐用法

traj 应为每条 synthetic session 维护一条 Claude Code session id：

```text
first assistant turn:
  claude --bare -p --output-format json <prompt>
  → capture claude_session_id

next assistant turns:
  claude --bare -p --resume <claude_session_id> --output-format json <next_user_msg>
```

建议使用 `--bare`，减少本地 hooks、skills、MCP 自动发现、memory 等对合成数据的非预期影响。若未来需要更强的跨机器稳定性，可升级到 Claude Agent SDK 的显式 session 管理；但第一阶段应优先用 Claude Code CLI，贴近真实 assistant coding agent 行为。

### 注意事项

- CLI session persistence 是本地状态，和 cwd/运行机器有关；并发 synthetic sessions 必须各自记录 session id。
- session 恢复的是对话与工具历史，不是文件系统快照；sandbox 目录仍需由 traj 管理。
- 如果在容器/临时 runner 中运行，需要把 session 存储位置、sandbox、artifact 路径作为同一 session bundle 管理。
- 不应让 TokenKey 管理 Claude Code session；TokenKey 只观察经过网关的模型请求。

## Recommended implementation path

### Phase 1 — 收敛调用路径与元数据边界

目标：让 traj 合成调用尽可能像真实用户调用。

TokenKey 侧：

1. 保持真实用户路径不变，只确认 synth metadata 不破坏现有 API 行为。
2. 将 `X-Synth-*` 视为可选观测元数据，不改变调度逻辑。
3. 用专用 key/group/account 池做隔离，而不是新增 traj-only 路由。
4. 明确 `trajectory_id`、`request_id`、`synth_session_id` 的关联关系。

traj 侧：

1. user adapter 和 assistant adapter 都走 TokenKey 标准入口。
2. assistant adapter 从 Messages API shim 演进到 Claude Code `-p` headless。
3. 每个 synthetic session 显式记录：TokenKey synth session id、Claude Code session id、sandbox path、export artifact path。

### Phase 2 — TokenKey 做 substrate 级契约与低成本导出清理

目标：TokenKey 只保证生产路径可信、导出可信、清理可信。

TokenKey 侧：

1. 固化 `trajectory.jsonl` 基础 schema/version，但只覆盖 export substrate，不覆盖深度质量分级。
2. 将 `scripts/check-traj-dataset.py` 定位为结构性 gate，而不是最终质量 gate。
3. 强化 hook/capture drift check，优先放在脚本和 companion 中，减少 upstream 文件改动。
4. 复用并小幅加固现有 prod export + purge 操作链路：导出到本地成功、manifest/count/checksum 校验通过后清理在线 DB/S3/EBS 过程文件。

traj 侧：

1. 只消费 TokenKey 导出的本地 artifact。
2. 不要求 TokenKey 长期保存训练数据。
3. 对导出 manifest/checksum 做二次确认，再进入 grading/cleaning pipeline。

### Phase 3 — traj 承接质量分级、分档、清洗提升

目标：把数据好坏判断全部收敛到 traj。

traj 侧：

1. 保持 transcript 为回合编排真相。
2. 将 TokenKey QA/export 作为 evidence/meta 输入，而不是 turn reconstruction 输入。
3. 建立 bronze/silver/gold 或等价分档策略。
4. 将 C1–C5 扩展为“结构 + 行为 + 成本 + 质量”分层 gate。
5. 对低质量 session 做清洗、裁剪、拒收或重跑。

TokenKey 侧：

1. 不引入分档/清洗/评分业务逻辑。
2. 只提供足够稳定的 raw evidence 和基础投影。

### Phase 4 — 双仓 E2E 回归

目标：一条 synthetic session 能以最少差异走完整生产路径，并在本地完成质量闭环。

验收链路：

```text
需求/spec
  → traj user adapter calls TokenKey
  → traj assistant adapter uses Claude Code -p and calls TokenKey
  → TokenKey captures production evidence
  → prod export script copies artifact to local
  → successful local validation triggers prod purge
  → traj builds transcript-centered dataset
  → traj grades/cleans/tiers dataset
```

## Repo-specific improvement list

### TokenKey / sub2api

1. 保持真实调用与 synth 调用同构：不新增 traj-only gateway path。
2. 将 `X-Synth-*` 作为可选观测元数据，不改变真实调度语义。
3. 明确并校验 `trajectory_id` / `request_id` / `synth_session_id` 的最低关联保证。
4. 将 `trajectory.jsonl` schema/version 定位为基础导出契约，而不是完整训练数据契约。
5. 保留 H1/H2/H3/D1 等结构性 gate，但不要扩展成深度质量评分器。
6. 复用 `scripts/fetch-prod-qa-dump.sh` + `scripts/prod-qa-export-and-purge.sh`，形成 prod QA buffer → local artifact → manifest/count/checksum → prod purge 的极简操作链路。
7. 当前 purge 是全量 QA buffer 清理，必须在专用 synth 采集窗口或低峰窗口运行；未来精确 scope purge 只能在脚本层按 manifest 演进，避免污染服务主路径。
8. hook/capture drift check 尽量脚本化、语义化，减少 upstream-owned Go 文件变更。

### traj

1. user/assistant adapter 都走 TokenKey 标准调用路径，只通过 metadata 标记 synth。
2. assistant adapter 迁移到 Claude Code `-p` headless，并显式维护每条 synthetic session 的 Claude session id。
3. 保持 transcript 为唯一回合编排真相；TokenKey evidence 只补 meta/evidence。
4. 承接深度质量校验、分级分档、清洗提升、拒收/重跑策略。
5. 将 C1–C5 从基础 verify 扩展为面向训练价值的离线质量 pipeline。
6. 消费本地导出 artifact，而不是依赖 TokenKey prod 长期保存数据。
7. 管理 sandbox、Claude Code session、TokenKey synth session、export artifact 之间的 manifest。

## Key files and minimal-conflict extension points

### TokenKey

- `backend/internal/server/routes/gateway.go`
  - 仅保留薄注入点：`middleware.TrajectoryID()`、`h.QACapture.Middleware()`。
- `backend/internal/observability/qa/sse_tee.go`
  - 维持 terminal capture：`Service.CaptureFromContext(...)`。
- `backend/internal/observability/trajectory/projection.go`
  - 维持基础 export projection，不承载深度质量评分。
- `backend/internal/observability/qa/service_traj_export.go`
  - self/prod export 的服务层入口。
- `scripts/check-traj-dataset.py`
  - 结构性 gate。
- `scripts/check-trajectory-hooks.py`
  - hook/capture drift gate。
- `scripts/fetch-prod-qa-dump.sh`
  - 现有 prod QA 只导出入口：SSM 打包 `qa_records` + `qa_blobs`，S3 presigned staging，本地解压到 `./.dump_trajs/`，写 manifest/count/checksum。
- `scripts/prod-qa-export-and-purge.sh`
  - 现有 prod export + cleanup 主入口：复用导出脚本，本地校验通过后清理 prod `qa_records`、`qa_blobs`、`qa_dlq` 与 S3 staging object。

### traj

- `../traj/pipeline/runtime/orchestrator.py`
  - session/turn lifecycle 真相。
- `../traj/pipeline/runtime/cursor_user.py`
  - user-side adapter，可继续向真实 Cursor Agent CLI 演进。
- `../traj/pipeline/runtime/claude_assistant.py`
  - assistant-side adapter，应向 Claude Code `-p` headless 演进。
- `../traj/pipeline/runtime/build_traj.py`
  - transcript-centered dataset builder。
- `../traj/pipeline/verify.sh`
  - 离线质量 gate 总入口。
- `../traj/pipeline/schemas/traj_v1.json`
  - traj dataset schema。

## Verification

### TokenKey substrate verification

- trajectory hook/capture drift check 通过。
- dataset structural gate 通过。
- prod export 生成本地 artifact 后 manifest/count/checksum 校验通过。
- purge dry-run 与真实执行均符合当前脚本语义：全量 QA buffer 清理只在专用 synth/低峰窗口运行，失败时 prod 数据保持不变。
- purge 后线上 DB/S3/过程文件不再保留已导出 synth 数据。

### traj orchestration and quality verification

- transcript 从 user 开始、assistant 结束，turns 连续且可重放。
- Claude Code `-p` session id 能跨 assistant turns resume。
- sandbox 与 Claude session 一一对应，不串 session。
- C1–C5 通过，并能输出质量分档/拒收原因。
- 清洗/提升后的 dataset 仍保留原始 manifest 与 evidence reference。

### End-to-end verification

- synth user 与 assistant 均走 TokenKey 标准调用路径。
- TokenKey 能捕获两类调用的 QA evidence。
- 本地 artifact 可被 traj 消费并完成 build/grade/clean。
- 成功导出后 prod 数据被清理，失败时不清理。
- 整条链路没有 traj-only gateway path，没有 TokenKey-side dataset judge。

## Risks and controls

- **风险：为了方便给 traj 开旁路。** 控制：强制走真实 TokenKey 调用路径，只加 metadata。
- **风险：TokenKey 变成质量评分系统。** 控制：TokenKey 只做结构性 substrate gate，深度质量放 traj。
- **风险：traj transcript 与 TokenKey evidence 分叉。** 控制：定义 manifest，将 transcript session、TokenKey request/trajectory、Claude session id 绑定。
- **风险：prod purge 误删或漏导 QA buffer 新增数据。** 控制：当前脚本按全量 QA buffer 清理，必须在专用 synth/低峰窗口运行；先 dry-run，必要时设置 `PURGE_MAX_EXTRA_ROWS=0`，未来精确 scope purge 只能在脚本层按 manifest 演进。
- **风险：Claude Code CLI session 本地态导致并发串线。** 控制：每条 synthetic session 独立 cwd/sandbox/session id，禁止依赖 `--continue` 处理并发，优先使用 `--resume <session_id>`。
- **风险：upstream merge 冲突扩大。** 控制：TokenKey 改动优先脚本、companion、薄注入点，不改 upstream 大段逻辑。
