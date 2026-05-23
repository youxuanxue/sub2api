---
name: tokenkey-anthropic-oauth-config
description: >-
  TokenKey Anthropic 配置写入流水线（snapshot → check → plan → apply → verify）。
  **三条写入面**，都由同一个脚本 ops/anthropic/manage-anthropic-config.py 编排，且都
  "JSON 派生 SQL、无静态模板、operator 不写 SQL"：
  (A) edge anthropic OAuth account 的 tier baseline（concurrency / base_rpm /
  sticky_buffer / max_sessions 等 account 字段）—— 来源
  anthropic-oauth-stability-baselines-tiered.json；同一事务把 users.id=1 的
  concurrency 更新为该 edge 库内 schedulable=true 的 anthropic 账号 concurrency 之和。
  (B) prod anthropic api-key 镜像 stub（base_url=api-*.tokenkey.dev 形状）的
  credentials.pool_mode + pool_mode_retry_count —— 来源 anthropic-stub-pool-baselines.json。
  (C) prod stub concurrency 镜像（plan-concurrency-mirror）：把 edge users.id=1 与
  对应 prod stub.concurrency 与 prod users.id=1 都对齐为「Σ schedulable=true anthropic
  concurrency」的四跳级联——值从 live 派生，不引入新 baseline JSON；stub↔edge 链接按
  edge-targets.json 的 domain 字段稳定匹配，不推断。
  group.rpm_limit 不由本流水线写——admin UI 直接独立设置。
---

# TokenKey：Anthropic 配置流水线

适用于本仓库（TokenKey fork of sub2api）。三条**完全平行**的写入面，都由 `ops/anthropic/manage-anthropic-config.py` 编排，**都遵守同一组确定性纪律**（详 §0）：

| 写入面 | 数据真值源 | action.kind | 影响范围 |
|---|---|---|---|
| (A) edge OAuth tier baseline | `anthropic-oauth-stability-baselines-tiered.json` | `edge_account_tier` | 每个 deployable edge 上 `type=oauth` 账号的 `extra.*` + `concurrency` + `priority` + `stability_tier` 字段；事务末尾同步 `users.id=1.concurrency = Σ schedulable anthropic.concurrency` |
| (B) prod stub pool_mode | `anthropic-stub-pool-baselines.json` | `prod_stub_pool` | 每个 prod `type=apikey` 且 `credentials.base_url` 匹配 `api-*.tokenkey.dev` 的镜像 stub 的 `credentials.pool_mode` + `credentials.pool_mode_retry_count` 字段 |
| (C) prod stub concurrency 镜像 | **无 JSON——值从 live 派生**；stub↔edge 链接来自 `edge-targets.json` 的 `domain` | `edge_operator_concurrency` + `prod_concurrency_mirror` | 四跳级联：每个 deployable edge 的 `users.id=1.concurrency`、对应 prod stub 的 `concurrency`、prod 的 `users.id=1.concurrency` 全部对齐为「该库 Σ schedulable=true anthropic concurrency」 |

写入面 (A)/(C) 都把 **`users.id=1` 的用户并发** 对齐为该库 **`platform=anthropic AND schedulable=true` 账号** concurrency 之和（**不含**被 admin 设为 `schedulable=false` 的账号，例如 edge 上仅供 admin 旁路的 api-key）；**prod 控制面**（admin 网页创建/更新/删除账号、批量编辑）由 **`AdminService` + `SumConcurrencyAnthropic` / `SyncAnthropicOperatorConcurrency`** 在写库成功后执行**相同** Σ 规则并 `BatchSetConcurrency`。脚本与后端控制面共用同一条 Σ 规则（`SumConcurrencyAnthropic` 已加 `AND schedulable = true`），互不覆盖。流水线固化在 `ops/anthropic/manage-anthropic-config.py`（edge + prod SSM apply）与后端 `anthropic_operator_concurrency.go`（共享语义）。

`group.rpm_limit` **不由本流水线写**——admin UI 独立设置。

## 0. 确定性硬纪律（适用于两条写入面）

本 skill 的核心承诺：**operator 不写 SQL、不靠记忆字段、不靠列号读取**。所有"可能幻觉"的环节都对应一个固化机制——破任何一条都属于 bug。

| 风险 | 固化机制 | 触发文件 |
|---|---|---|
| 数据值散落多处、修改后漏改 | 写入值**只存在 baseline JSON 一处**；apply SQL 由 orchestrator 运行时派生 | `anthropic-oauth-stability-baselines-tiered.json` / `anthropic-stub-pool-baselines.json`；不存在 `*.sql` 模板 |
| 远端 SQL 输出靠列号读取（坑 6） | 所有远端 SELECT 用 `jsonb_agg(jsonb_build_object(...))`，字段名贴在值旁 | `EDGE_ACCOUNTS_SQL` / `PROD_STUBS_SQL` in `manage-anthropic-config.py` |
| operator 现场拼 WHERE 写错行 | 渲染器在 `id + name + platform + type + deleted_at IS NULL` 五重定位，且 `account_id`/`account_name` 都来自 plan 而非 CLI | `render_edge_account_tier_sql` / `render_prod_stub_pool_sql` |
| 误把 `extra` 当 credentials / 反之 | snapshot SQL 显式区分：`extra.*` 走 `tier baseline` 通道，`credentials.*` 走 `stub_pool` 通道，两者各有自己的 JSON 真值源 + render 函数 | 同上 |
| apply 没写到/写错行 | 渲染 SQL 末尾 `RETURNING id, name, after_*` 列，stdout 落 `apply-report.json` 留档；Stage 5 verify 独立再 snapshot 对比 | `cmd_apply` / `cmd_verify` |
| 跨账号脑补现状 | snapshot 总是先拉**一次** SSM live，plan 派生于 snapshot，不允许凭"我记得它是 …" 现场断言 | `_load_snapshot_or_die` 强制版本号校验 |
| 误触发的破坏性 apply | `--confirm yes-apply-anthropic-config-cascade` 字面匹配；缺失或拼错都 fail | `CONFIRM_CODE` 常量 |
| 单元测试漂移 | `_TIER_BASELINE_FIELDS` 是 plan/apply/verify 共用的字段集；新增字段加一处即可（不会出现"plan 写了 verify 不看"） | `test_manage_anthropic_config_plan.py` / `*_stub_pool.py` |

**新增检查项 / 新增写入面的硬纪律**：必须同时落 (a) baseline JSON 字段、(b) snapshot SQL 列、(c) render 函数、(d) verify 比对、(e) 单元测试。少一项即视为半成品；review 时拒收。

> ⚠️ 此条目前为软规则——机械化 lint（每条 `action.kind` ↔ §0 表行映射 + 五层落地交叉检查）在 dev-rules backlog 中跟进；在此之前发现违例的代价由 reviewer 承担。这是 OPC "本可机械化但暂留 prose" 的已知缺口，明示在此避免隐式承诺。

需要查"现在 live 状态"时**只用 `manage-anthropic-config.py snapshot`**——不要现场拼 psql/redis-cli。临时排障如必须直查，遵循同源 traffic-profile skill §1.1 的 row_to_json 固化脚本流程，不要写多列 `\|` 分隔 SELECT。

**users.id=1 并发对齐（细节）**：每次 `apply` 对某 edge 执行一条 tier-baseline SQL 事务时，事务在 `COMMIT` 前附带 `UPDATE users`：把 `users.id = 1`（`deleted_at IS NULL`）的 `concurrency` 设为 `SUM(accounts.concurrency)`，筛选与 prod 相同：`platform = 'anthropic' AND schedulable = true AND deleted_at IS NULL`（**只数 schedulable=true** 的 anthropic 行——admin 旁路用的 `schedulable=false` api-key 不计入）。多账号 plan 连续 apply 时每一步后都会重算总和，末步即最终一致。手工 `check-edge-oauth-stability.py --emit-sql` 生成的 SQL **不含** 此段；需用本脚本 `apply` 或自行追加同等语句。**写入面 (B) prod_stub_pool 不动 concurrency**——它写的是 stub credentials JSONB 的 `pool_mode` / `pool_mode_retry_count` 两个键，不影响 Σ。**写入面 (C) prod_concurrency_mirror 只改 `concurrency` + `updated_at`**——同样不碰 credentials JSONB，所以 (B) 的 pool_mode / pool_mode_retry_count 在 (C) apply 后原样保留。后端 Go 控制面 `SumConcurrencyAnthropic` 也用**同一条** `schedulable = true` Σ 规则——admin 网页改账号触发的 sync 不会与脚本写入的值互相覆盖。

权威纪律以仓库根 `CLAUDE.md` 为准。

## 1. 5 阶段流水线

每阶段一个命令，输入/输出明确，失败即停。所有写入通过 JSON 派生的 SQL（无静态 SQL 模板），operator 不写 SQL。

```bash
JOBDIR="$CLAUDE_JOB_DIR"               # or any scratch dir
MGR=ops/anthropic/manage-anthropic-config.py

# Stage 1 — Snapshot：拉所有 deployable edge 的 anthropic OAuth account + prod anthropic api-key stub 状态到 JSON
python3 $MGR snapshot --out $JOBDIR/snap.json

# Stage 2 — Check：每个 edge 跑一次 edge-oauth-stability guard，union 报告
python3 $MGR check --snapshot $JOBDIR/snap.json

# Stage 3 — Plan：声明变更意图，机器算 expected_after
#   3a) 移单个账号到某 tier（不改 JSON 数值）：
python3 $MGR plan-edge-account-tier \
  --edge uk1 --account en-ld-ec2-16-1-b --tier l2 \
  --snapshot $JOBDIR/snap.json --out $JOBDIR/plan.json
#   3b) 改某 tier 的基线值本身 → 一个多 action plan 覆盖该 tier 全部账号：
python3 $MGR plan-tier-bump --tier l5 \
  --snapshot $JOBDIR/snap.json --out $JOBDIR/plan.json
#   3c) 给所有 prod 镜像 stub 开 pool_mode（base_url 形状 api-*.tokenkey.dev）：
python3 $MGR plan-stub-pool \
  --snapshot $JOBDIR/snap.json --out $JOBDIR/plan.json
#   3d) 把 edge users.id=1 / prod stub.concurrency / prod users.id=1 对齐为 Σ schedulable：
python3 $MGR plan-concurrency-mirror \
  --snapshot $JOBDIR/snap.json --out $JOBDIR/plan.json

# Stage 4 — Apply：渲染 SQL → SSM → 写入；失败即停
python3 $MGR apply \
  --plan $JOBDIR/plan.json \
  --confirm yes-apply-anthropic-config-cascade

# Stage 5 — Verify：再 snapshot + 比对每个 action 的 expected_after vs live
python3 $MGR verify --plan $JOBDIR/plan.json
```

### 各阶段语义

| 阶段 | 输入 | 输出 | exit |
|---|---|---|---|
| snapshot | EC2 SSM 权限 | `snap.json`：`edges.*.oauth_accounts` + `prod.anthropic_stubs`，**字段名嵌在值旁**（jsonb_agg） | 0 / 2 error |
| check | snap.json | 每个 edge 跑 `check-edge-oauth-stability.py`，union violation | 0 ok / 1 violation / 2 error |
| plan-edge-account-tier | snap.json + edge+account+tier | `plan.json`：1 个 `kind=edge_account_tier` action | 0 / 2 |
| plan-tier-bump | snap.json + tier | `plan.json`：N 个 `kind=edge_account_tier` action（该 tier 全部账号，跨 deployable edge；已匹配跳过） | 0 / 2 |
| plan-stub-pool | snap.json | `plan.json`：N 个 `kind=prod_stub_pool` action（base_url 匹配的全部 prod stub；已匹配跳过、非匹配单列 `skipped_unmatched`） | 0 / 2 |
| plan-concurrency-mirror | snap.json | `plan.json`：每个漂移 edge 一个 `kind=edge_operator_concurrency` action + （若有 stub/prod operator 漂移）一个 `kind=prod_concurrency_mirror` action；全对齐则 `noop=true` | 0 / 2 |
| apply | plan.json + confirm | 逐 action 渲染 SQL → SSM 执行（按 kind 路由到 edge 或 prod）→ 写 `apply-report.json`；任一 step 失败即停 | 0 / 1 step failed / 2 |
| verify | plan.json | 再 snapshot + 比对**每个** `actions[*].expected_after` vs live；drift 列表 | 0 / 1 drift / 2 |

### snapshot JSON 结构速查

解析 `snap.json` 别猜形状（`edges` 是**按 edge_id 索引的 dict**，不是 list；edge 账号在 **`oauth_accounts`**；prod stub 在 `prod.anthropic_stubs`，独立顶层 key）：

```jsonc
{
  "version": <int>, "captured_at": "...Z",
  "edges": {
    "us1": {                       // key = edge_id
      "deployable": true, "instance_id": "i-...", "region": "...",
      "oauth_accounts": [          // ← edge OAuth 账号在这里
        { "id": 1, "name": "...", "stability_tier": "l5",
          "base_rpm": 28, "rpm_sticky_buffer": 20, "concurrency": 10,
          "max_sessions": 100, "window_cost_limit": 1500, "status": "active",
          "schedulable": true, ... }
      ]
    },
    "uk1": { "deployable": false, "skipped_reason": "planned; pass --allow-planned" }
  },
  "prod": {                        // ← 顶层；不嵌在 edges 里
    "instance_id": "i-...", "region": "us-east-1",
    "domain": "api.tokenkey.dev",
    "anthropic_stubs": [           // ← prod 全部 anthropic api-key 账号（含非镜像 stub；plan-stub-pool 用 base_url regex 过滤）
      { "id": 42, "name": "cc-us1", "type": "apikey", "status": "active",
        "schedulable": true, "concurrency": 16,
        "cred_base_url": "https://api-us1.tokenkey.dev",
        "cred_pool_mode": true,
        "cred_pool_mode_retry_count": 1 }
    ]
  }
}
```

planned / 未快照的 edge 带 `skipped_reason`（或 `error`）且无 `oauth_accounts`——遍历时跳过它们（`plan-tier-bump` 已自动跳过）。`prod.error` / `prod.skipped_reason` 同理；`plan-stub-pool` 见到会 fail-loud 要求重新 snapshot。

### 失败即停 + Pre-apply re-read

- `apply` 任一 step 失败 → STOP，`apply-report.json` 列出已完成 + 未完成 step
- Stage 5 verify 必须跑；drift → operator 决定补 apply 或回滚
- snapshot 出于"先查后说"原则：禁止凭记忆断言字段值，所有断言都来自一次 SSM read

### 两种 plan：移单个账号 vs 改整个 tier 的值

- **`plan-edge-account-tier`** —— 把**一个账号**从当前 tier **移到另一个 tier**（不改 JSON 数值）。纯 live 写入，无 JSON/PR 跟进。
- **`plan-tier-bump`** —— 改**某个 tier 的基线数值本身**（如 L5 `max_sessions` 50→60）。这是 tier 级变更，影响**该 tier 上的每一个账号、跨所有 deployable edge**。

> ⚠️ **改 tier 值时不要用 `plan-edge-account-tier` 逐个手敲账号**——很容易漏掉某个 edge 上的同 tier 账号，导致它静默停在旧值，下次 `check` 报 `extra_baseline_drift`。用 `plan-tier-bump`：它从 snapshot 枚举该 tier 的**全部** live 账号，产出**一个多 action plan**；`apply` / `verify` 本来就迭代 actions 数组，所以**一次 apply + 一次 verify** 覆盖整批。

### 写入面 (B) 配方：给所有 prod 镜像 stub 开 pool_mode

**触发场景**：prod 端命名形如 `cc-<edge>`（或更早的 `cc-<edge>-oauth`，**改名不影响**——匹配按 `credentials.base_url`）的 anthropic api-key stub，其 `credentials.base_url` 指向对应 edge 域名（`https://api-<edge>.tokenkey.dev`）。这些 stub 被 prod 路由层的 `anthropic_upstream_error` 关键词阈值规则 cooldown 是 OPC 系统级链式失败的主要根因（详 traffic-profile skill §0 坑 7）。pool_mode 让 prod 端**同账号原地重试 N 次**触发 edge 池内轮换，**不**写 `temp_unschedulable_until`，**且**额外跳过 `handleAnthropicUpstreamError` 的 3/3 短窗阈值（仅 anthropic 平台 + pool_mode 账号双重生效；详 `backend/internal/service/account.go::IsPoolMode` + `ratelimit_service.go`）。

**唯一前提**：stub 必须 `type='apikey'`（pool_mode 的 `IsAPIKeyOrBedrock()` 前置检查；oauth 类型永远启用不了）。命名后缀 `-oauth` 或不带后缀都不影响，匹配按 `credentials.base_url` regex。

策略：**所有匹配 base_url pattern 的 prod stub 都开**（含 status=disabled 的——后续 admin 重启时直接带着 pool_mode 上来）。**不**按 "edge 上有几个可调度账号" 做门禁——pool_mode 在单账号场景下也有正向价值（同账号 retry 仍优于直接 cooldown）。

```bash
# 0) baseline JSON 已固化在仓库 — 一般不需要改；如要改默认 retry_count：
#    编辑 deploy/aws/stage0/anthropic-stub-pool-baselines.json 的 policy.pool_mode_retry_count
python3 $MGR snapshot --out $JOBDIR/snap.json
# 1) 枚举 prod 全部匹配的 stub → 多 action plan（已开启的 stub 自动 noop 跳过）
python3 $MGR plan-stub-pool \
  --snapshot $JOBDIR/snap.json --out $JOBDIR/plan.json
#    plan.json 含 summary.skipped_unmatched（哪些 base_url 没匹配，给 op 检视用）
# 2) apply + verify
python3 $MGR apply  --plan $JOBDIR/plan.json --confirm yes-apply-anthropic-config-cascade
python3 $MGR verify --plan $JOBDIR/plan.json                   # drift_count 必须=0
# 3) 若改了 baseline JSON 数值 → 分支 + PR 落 origin/main（仓库纪律 §5.y）
```

**为什么不需要写 SQL**：apply 把 plan 翻译成 `UPDATE accounts SET credentials = credentials || jsonb_build_object('pool_mode', true::boolean, 'pool_mode_retry_count', N::int) WHERE id = ... AND name = ... AND platform = 'anthropic' AND type = 'apikey' AND deleted_at IS NULL RETURNING ...`——`id+name+platform+type` 五重定位防止写错行；`||` JSONB merge 保留 credentials 里其他键（api_key、base_url 等）原样；`RETURNING` 让 apply-report.json 留下确证。SQL 文件会落在 `apply-report.json` 同目录的 `step01-prod-prod_stub_pool-<name>.sql` 留档。

**特殊情况 — 新增 prod stub 之后**：admin UI 新建一个新的 `cc-<新edge>`（`type='apikey'`，`credentials.base_url='https://api-<新edge>.tokenkey.dev'`）后，**不要手开** pool_mode。流程是：
1. 新 stub 通过 admin UI 创建，pool_mode 默认 null（关）。
2. `snapshot + plan-stub-pool` 会自动把它识别为待开（live cred_pool_mode != true）→ action 列表 +1。
3. `apply + verify`。
4. 整轮收尾：admin UI 上能看到「池模式」开关已置位。

`plan-stub-pool` 输出 `noop=true`（0 action，但 `skipped_noop` 非零）说明所有匹配 stub 已是策略值；要强制重写加 `--force-template-rewrite`（一般只用于排查 credentials 脏写）。

### 写入面 (C) 配方：prod stub concurrency 镜像（四跳级联对齐 Σ schedulable）

**触发场景**：调过 tier baseline（如 L4 `concurrency` 6→8）抬高了某 edge 的可调度 OAuth 池容量后，**prod 的镜像 stub `concurrency` 不会自动跟进**——它历史上镜像「对应 edge 的可服务并发容量」，让 prod 不会向 edge 派发超过其 OAuth 池能承载的并发。这条镜像在 2026-05-21 连同已废弃的 group RPM 聚合一起被移除，本面在 2026-05-23 重新接回，**口径统一为「Σ schedulable=true anthropic concurrency」**（不含 admin 旁路的 `schedulable=false` api-key）。

**四跳级联**（全部对齐为各库的「Σ schedulable=true anthropic concurrency」）：

| # | 写入对象 | action.kind | target |
|---|---|---|---|
| 1 | edge 各 anthropic 账号 config | （写入面 A，已存在） | edge |
| 2 | edge `users.id=1.concurrency` | `edge_operator_concurrency` | edge |
| 3 | prod 对应 stub `.concurrency` | `prod_concurrency_mirror`（同一 prod 事务，先于 #4） | prod |
| 4 | prod `users.id=1.concurrency` | `prod_concurrency_mirror`（子查询，权威，在 #3 之后） | prod |

**stub↔edge 链接**：按 `deploy/aws/stage0/edge-targets.json` 每个 edge 的 `domain` 字段（如 `api-us1.tokenkey.dev`）匹配 prod stub 的 `credentials.base_url`——**稳定读取部署配置，不做 slug 推断、不每次重新校验拓扑**。匹配不上的 stub（tokensea / deepseek 等第三方）不进 `stub_updates`。

```bash
python3 $MGR snapshot --out $JOBDIR/snap.json
# 派生：每个 deployable edge 的 Σ schedulable 与 live operator concurrency 比对（漂移→hop2 action），
#       对应 prod stub.concurrency 与 edge Σ 比对（漂移→hop3），prod operator 重算（hop4）
python3 $MGR plan-concurrency-mirror \
  --snapshot $JOBDIR/snap.json --out $JOBDIR/plan.json
#    人工核对 plan：edge_operator(us1→Σ) + prod_concurrency_mirror(cc-us1→Σ, prod op→新值)
python3 $MGR apply  --plan $JOBDIR/plan.json --confirm yes-apply-anthropic-config-cascade
python3 $MGR verify --plan $JOBDIR/plan.json                   # drift_count 必须=0
```

**安全栏**：某 edge 的 Σ schedulable=0 时**跳过**该 edge / stub 并 loud-warn——**绝不把 stub 或 operator concurrency 写成 0**（那会静默抽干该链路）。`prod_concurrency_mirror` 的渲染对每个 stub int 校验（拒绝 <1）+ 五重 WHERE（id+name+platform+type+deleted_at）+ name 单引号转义，与写入面 (B) 同等防御。**只写 `concurrency` + `updated_at`**，不碰 credentials JSONB，所以 (B) 的 pool_mode 配置不受影响。

`plan-concurrency-mirror` 输出 `noop=true` 说明四跳已全对齐；要强制重写已对齐的也纳入加 `--force-template-rewrite`（排障用）。

> 不引入新 baseline JSON：级联值全部从 live `schedulable` + `concurrency` 确定派生，无可调参数。

### 改 tier baseline 值（如 L5 max_sessions 50→60）的完整 recipe

```bash
# 0) 先编辑唯一真值源（apply 时只读它派生 SQL）
#    deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json
#    把 tiers.l5.baseline.extra.max_sessions 改成新值
python3 $MGR snapshot --out $JOBDIR/snap.json
python3 $MGR check     --snapshot $JOBDIR/snap.json          # 改前基线全绿
# 1) 枚举该 tier 全部账号 → 一个多 action plan（fields 已匹配的账号自动跳过）
python3 $MGR plan-tier-bump --tier l5 \
  --snapshot $JOBDIR/snap.json --out $JOBDIR/plan.json
# 2) 一次 apply + 一次 verify 覆盖整批
python3 $MGR apply  --plan $JOBDIR/plan.json --confirm yes-apply-anthropic-config-cascade
python3 $MGR verify --plan $JOBDIR/plan.json                  # drift_count 必须=0
# 3) JSON 改动经 分支 + PR 落 origin/main（见下）
```

`plan-tier-bump` 输出 `noop=true`（0 action）说明该 tier 当前账号字段已全等新值（多半是你已 apply 过又重跑）；要强制重写 credentials 端字段加 `--force-template-rewrite`。

**必跟的一步：提交 JSON。** 若你为改 tier 基线值而**编辑了这个 JSON**，apply 到 live 后**必须把 JSON 改动经分支 + PR 落到 `origin/main`**（仓库纪律 §5.y，不直推）。否则：

- 本地 JSON=新值、live=新值 → 你本地 `check` 通过；
- 但 `origin/main` 仍是旧值 → 别人 fresh checkout / CI 跑 `check-edge-oauth-stability` 会把 live 报成 `extra_baseline_drift`（live↔repo 漂移）。

**不需要联动改 Go 代码。** preflight 的 `anthropic baseline sync` 检查（`scripts/sentinels/check-anthropic-baseline-sync.py`）只比对 **`policy.cooldown_tier_ttl_minutes`** 一个字段 ↔ `ratelimit_service.go` 常量；per-tier 的 `baseline.account.*` / `baseline.extra.*`（`max_sessions` / `base_rpm` / `rpm_sticky_buffer` / `concurrency` / `window_cost_limit` …）**都不镜像到 Go**，所以改这些 tier 值永远不触发该 check，无需改代码。

## 2. 不在本流水线范围内（独立操作）

本流水线**三条**写入面（详 §0 表格）：
- (A) edge OAuth tier baseline `extra.*` + `concurrency` + `priority` + `stability_tier` + 同事务 `users.id=1.concurrency` Σ 对齐
- (B) prod anthropic api-key 镜像 stub 的 `credentials.pool_mode` + `credentials.pool_mode_retry_count`
- (C) prod stub concurrency 镜像：edge `users.id=1` + 对应 prod stub `.concurrency` + prod `users.id=1` 四跳对齐 Σ schedulable

下列写入面**不由本脚本管**：

| 配置面 | 写入方式 |
|---|---|
| edge / prod `group.rpm_limit` | admin UI 直接编辑；operator 凭运维经验定独立绝对值，与 account 字段解耦 |
| edge / prod 任何 `group` 字段 | admin UI |
| prod anthropic apikey stub 的 **其他** 字段（`base_url` / `api_key` / `concurrency` / 名字 …） | admin UI |
| edge OAuth `account_groups` 绑定（哪个账号进哪个组） | admin UI |
| prod anthropic stub `account_groups` 双绑（default + cc-edges，见 § 3） | admin UI |
| OAuth 凭据轮换 / status | admin UI / OAuth flow |

**与 admin UI 的边界**：本流水线只写 **(B) 写入面里那两个 credentials 子键**——其他 stub 字段（含 `base_url` 本身）仍归 admin UI。pool_mode 是数据集 (`apikey` + `base_url` 形状) 决定的**策略性配置**，不像 base_url 那样是个体性的，所以集中化；其他字段都是个体性的，仍走 admin UI。

历史上 2026-05-21 之前曾尝试用本脚本做"account → group 聚合 cascade"（edge group cap = Σ(base_rpm+sticky_buffer)，prod stub.declared_rpm 镜像 edge group cap，prod group cap = Σ declared_rpm），但层层聚合导致 upstream OAuth 实际容量被压在 `Σ(base_rpm+sticky_buffer)` 上界、sticky buffer 黄区 burst 跑不出来。该模型已退役，group 限流回归 operator 手工独立设定。**2026-05-23 新增 (B) 写入面**：起因是 prod `cc-us1` 反复被 `anthropic_upstream_error` 关键词阈值规则 cooldown（edge-us1 透传 503 链式失败），手动开 pool_mode 验证有效后纳入流水线，避免新增镜像 stub 时漏开。**同日新增 (C) 写入面**：stub concurrency 镜像（曾随上面退役的 group RPM 聚合一起被移除）重新接回，**口径改为 Σ schedulable=true**（不含 admin 旁路 api-key），同时把后端 Go `SumConcurrencyAnthropic` 也加上 `AND schedulable = true` 让脚本与控制面共用同一条 Σ 规则、互不覆盖；**不**恢复已废弃的 group RPM 聚合。

## 3. prod 控制面：anthropic stub 双绑规则

prod 上每一个 anthropic 转发 stub（`platform=anthropic AND type=apikey`，credentials 含 `base_url=api-<edge>.tokenkey.dev`）必须**同时绑两个分组**：

| 分组 | id | 用途 | 谁可见 |
|---|---|---|---|
| `default` | 1 | 对外用户流量 | 普通用户 API key |
| `cc-edges` | 15 | admin 调试旁路 | 仅 admin API key |

可见性强制点：`groups.is_exclusive=true` + `user_allowed_groups` 白名单。`cc-edges` 必须 `is_exclusive=true`，admin user 在 `user_allowed_groups` 写入 `(admin_user_id, 15)`。

operate 流程：
- **新增 anthropic edge stub** → 同步 `INSERT account_groups` 双行（default + cc-edges）。两行不可拆开 apply。
- **退役 anthropic edge stub** → 软删 stub 前 `DELETE account_groups` 两行。
- **edge 内部** 真正的 OAuth 账号绑定 `default` 组即可，不复刻 `cc-edges`（admin 调试旁路只在 prod 控制面有意义）。

这些 binding 改动通过 admin 前端手动操作；本流水线**不**涉及。

## 4. 故障速查

| 现象 | 处理 |
|---|---|
| snapshot 失败 / SSM 拒绝（edge 或 prod） | 校验 EC2 instance 在跑 / `edge-targets.json` 或 `PROD_TARGET` 常量 / OIDC 权限。**仅排障 edge** 跑 `snapshot --skip-prod` 临时绕开 prod 失败 |
| `apply --confirm` 拒绝 | 必须精确 `yes-apply-anthropic-config-cascade` |
| tier baseline drift（check-edge-oauth-stability `extra_baseline_drift` / `account_field_drift`） | 单账号用 plan-edge-account-tier 重写到对应 tier；整个 tier 值漂移（多账号）用 `plan-tier-bump --tier lN` 一次性重写 |
| check guard 报 `status: drift` 且 `diffs[].path` 含 `/credentials/temp_unschedulable_rules`，但数值字段全等 | 加 `--force-template-rewrite` 让 plan 不再走 noop 短路，强制重写 credentials 端字段（snapshot/verify 不读 rules，所以默认 noop；这条 flag 是 escape hatch）。Apply 完跑一次 `check` 当真值 |
| OAuth account `status=error/suspended` | OAuth 凭据问题（token 过期 / 403 / 上游禁用），见 OAuth 故障文档；不在本流水线范围 |
| `plan-stub-pool` 输出 `skipped_unmatched` 含一个本应匹配的 stub | 检查它的 `cred_base_url` 实际值——多半是早期建账号时写错了（如多余 `/` 或大小写差异）。改 stub 的 `base_url` 通过 admin UI（**不**改 pattern；pattern 是策略，base_url 是个体配置）|
| 新增 `cc-<edge>` stub 后忘开 pool_mode | 跑一次 `snapshot + plan-stub-pool + apply + verify`；新 stub 会自动被识别为待开 |
| verify drift | operator 决定再 apply 或回滚（用 admin 前端按 plan.live_inputs.* 的 before 反向写回；prod_stub_pool 的反向 = admin UI 关掉「池模式」开关） |
| 想调 group.rpm_limit | admin UI 直接编辑 group，不要再用本流水线，也**不要**按 account 聚合手算 |

## 5. 附录 A：底层工具（emergency / debug）

正常流程**只走 5 阶段流水线**。下列工具在流水线 break 或紧急 rollback 时直接用：

**Guards**（只读检查）：
- `ops/anthropic/check-edge-oauth-stability.py --edge-id E --account-name A [--json] [--emit-sql FILE]` — edge OAuth tier baseline drift

**Apply SQL（JSON 派生，无静态模板）**：tier baseline 值只存在于 baseline JSON 一处；apply SQL 由 orchestrator 运行时从 JSON 渲染——内部复用 guard 的 `effective_baseline_for_tier`（合并 `shared_baseline` + tier 覆盖）+ `generate_sql`。手动 / 紧急生成同一份 SQL：
- `python3 ops/anthropic/check-edge-oauth-stability.py --edge-id E --account-name A --emit-sql out.sql`（按账号 live tier 渲染；改 tier 时先在 baseline JSON 调整对应 tier 值再渲染），再 base64 通过 SSM 注入。

**底线**：手动绕开 orchestrator 时 op 必须自己做 apply 后复核——同样不允许跳过 § 1 "先查后说"协议。

## 6. 附录 B：baseline JSON 速查与 stable accounts

- 写入面 (A) tier baseline：`deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`（`tier_order: l1..l5`，每个 tier 含 `baseline.account.*` 与 `baseline.extra.*`，orchestrator 在 plan-edge-account-tier / plan-tier-bump 中读它算 expected_after）
- 写入面 (B) stub pool baseline：`deploy/aws/stage0/anthropic-stub-pool-baselines.json`（`policy.base_url_pattern` regex + `pool_mode_enabled` + `pool_mode_retry_count`；orchestrator 在 plan-stub-pool 中读它算 expected_after）
- 更新 stable_accounts 列表（仅人工确认后）：
  ```bash
  python3 ops/anthropic/check-edge-oauth-stability.py \
    --edge-id $EDGE --account-name $ACCT \
    --update-stable-list --confirm yes-update-anthropic-stable-list
  ```
  禁止在未确认稳定前更新 stable list。

## 7. 扩展阅读

- `ops/anthropic/manage-anthropic-config.py`（5 阶段 orchestrator，本 skill 唯一推荐入口；含三条写入面 `edge_account_tier` + `prod_stub_pool` + `edge_operator_concurrency`/`prod_concurrency_mirror` 的渲染、apply、verify）
- `ops/anthropic/test_manage_anthropic_config_plan.py` / `test_manage_anthropic_config_stub_pool.py` / `test_manage_anthropic_config_concurrency_mirror.py`（plan 派生逻辑 + SQL 渲染 + apply 路由的单元测试，stdlib-only）
- `deploy/aws/stage0/edge-targets.json`（拓扑真值源：每个 edge 的 `domain` 是写入面 C 的 stub↔edge 链接依据，稳定读取不推断）
- `backend/internal/service/anthropic_operator_concurrency.go`（prod/控制面与脚本共享的 Σ schedulable→`users.id=1` 语义）
- `backend/internal/service/account.go` `Account.IsPoolMode()`（pool_mode 的运行时语义：apikey/bedrock 前置 + credentials.pool_mode 解析）
- `backend/internal/service/ratelimit_service.go` `handleAnthropicUpstreamError`（pool_mode 账号在 anthropic 平台上跳过 3/3 短窗阈值的代码路径）
- `ops/anthropic/check-edge-oauth-stability.py`
- `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`（tier baseline 唯一真值源；apply SQL 运行时从它派生，无静态 SQL 模板）
- `deploy/aws/stage0/anthropic-stub-pool-baselines.json`（stub pool 策略唯一真值源；apply SQL 运行时从它派生，无静态 SQL 模板）
- `backend/internal/handler/admin/account_handler.go`
- `backend/internal/service/admin_service.go`
- `backend/internal/repository/account_repo.go`
- `backend/internal/server/routes/admin.go`
