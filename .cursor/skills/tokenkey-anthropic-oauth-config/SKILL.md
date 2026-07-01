---
name: tokenkey-anthropic-oauth-config
description: >-
  TokenKey Anthropic OAuth read/check/remediation workflow. Use for manage-anthropic-config snapshot/check/sync-runtime, tk_canonical_cc_oauth TLS template drift, Claude Code UA/http mimicry sync, or OAuth stability guard checks across prod/edges.
---

# TokenKey：Anthropic OAuth 配置流水线（check 联查 + TLS 模板 + HTTP UA）

适用于本仓库（TokenKey fork of sub2api）。本 skill **不再是五条写入面的写库流水线**——tier 配置数值与其级联（operator 并发 Σ、pool_mode、concurrency 镜像、claude_code_only、edge 余额）已在 `origin/main` 下沉到**后端 per-node 自愈 reconciler + admin UI**（详 §3）。本 skill 现在只编排后端**不**负责的四件事，全部由 `ops/anthropic/manage-anthropic-config.py` 承载：

| 能力 | 子命令 | 写入面 | 后端为何不接管 |
|---|---|---|---|
| **check 联查**（只读） | `snapshot` + `check` | 无（只读 SSM live → 比对 baseline） | reconciler 只看本机库；跨 deployable edge + prod 的一次性 fleet 联查仍要 operator 触发 |
| **TLS canonical 模板** | `plan-guard-drift-fix` / `remediate-guard-drift`（+ `apply --sync-runtime`） | `tls_fingerprint_profiles` upsert + `accounts.extra.tls_fingerprint_profile_id` 绑定（force-template-rewrite） | reconciler 把单账号 tier 字段漂移设为 **report-only**（tier 经 admin UI ApplyTier 显式设定），TLS 模板的 fleet 重绑定不在自愈范围 |
| **HTTP UA / mimicry 同步** | `sync-runtime`（或 `plan-http-mimicry-sync` 出审计 plan） | `settings.claude_code_user_agent_version` + `settings.claude_code_http_mimicry_manifest` UPSERT；`DEL fingerprint:{oauth_account_id}` | settings/UA 是部署级运行时旋钮，reconciler 不写 settings 表 |
| **tier 值实时下发**（不发版） | `apply-tiers-live`（+ `--verify-only`） | 逐节点 `tiers` 表 UPDATE + Redis tier 缓存失效（`DEL tiers`/`PUBLISH tiers_updated`）+ `accounts.concurrency` UPDATE + `scheduler_outbox` + operator Σ 同步 | `ensureSeededFromBaseline` 只在**重启/发版**把 git 值 seed 进 tiers 表；把**已合并**的 git tier 值**实时**铺到全 fleet（不等发版）仍要 operator 触发。admin UI ApplyTier 是 per-account 手点，本工具是 **fleet + 从 git 单一源**。详 §2.5 |
| **OAuth 账号 email 机队审计**（只读） | `run-probe.sh` + `probe-account-emails.sh` | 无 | CC userEmail 回填依赖 `extra`/`credentials` email；缺 email 的 schedulable OAuth 账号需 Admin 补全或 ReAuth |

## 0. 确定性硬纪律

本 skill 的核心承诺仍是：**operator 不写 SQL、不靠记忆字段、不靠列号读取**。所有"可能幻觉"的环节都对应一个固化机制——破任何一条都属于 bug。

| 风险 | 固化机制 | 触发文件 |
|---|---|---|
| TLS 模板值散落多处、改后漏改 | 模板字段体**只存在 baseline JSON 一处**；upsert SQL 由 orchestrator 运行时派生 | `anthropic-oauth-stability-baselines-tiered.json` 的 `shared_baseline.tls_profile`（对照 `tk_canonical_cc_oauth.json`）；不存在 `*.sql` 模板 |
| HTTP UA / mimicry 值散落 | semver + manifest **只存在 baseline JSON 一处** | `deploy/aws/stage0/anthropic-http-mimicry-baselines.json`（`cc_version` / `sonnet_opus` / `haiku`）|
| 远端 SQL 输出靠列号读取（坑 6） | 所有远端 SELECT 用 `jsonb_agg(jsonb_build_object(...))`，字段名贴在值旁 | `EDGE_ACCOUNTS_SQL` / `PROD_STUBS_SQL` in `manage-anthropic-config.py` |
| operator 现场拼 WHERE 写错行 | 渲染器在 `id + name + platform + type + deleted_at IS NULL` 五重定位，且 `account_id`/`account_name` 来自 plan 而非 CLI | `render_edge_account_tier_sql`（guard-drift force-rewrite 复用） |
| 跨账号脑补现状 | snapshot 总是先拉**一次** SSM live，check/plan 派生于 snapshot，不允许凭"我记得它是 …" 现场断言 | `_load_snapshot_or_die` 强制版本号校验 |
| 误触发的破坏性 apply | `--confirm yes-apply-anthropic-config-cascade` 字面匹配；缺失或拼错都 fail | `CONFIRM_CODE` 常量 |

需要查"现在 live 状态"时**只用 `manage-anthropic-config.py snapshot`**（DB 持久化态）或 **`check`**（DB + Redis blob 比对，含 `redis_cache_drift`）——不要现场拼 psql/redis-cli。Redis 缓存与 DB 的一致性已由 `check` 的 `redis_cache_drift` 子项固化覆盖（`tls_fingerprint_profiles` / `tiers` blob vs DB 表），不再需要手搓 redis-cli 比对。临时排障如必须直查，遵循同源 traffic-profile skill §1.1 的 row_to_json 固化脚本流程，不要写多列 `\|` 分隔 SELECT。

`apply` 任一 step 失败 → STOP，`apply-report.json` 列出已完成 + 未完成 step。verify 必须跑；drift → operator 决定补 apply 或回滚。

权威纪律以仓库根 `CLAUDE.md` 为准。

## 1. TLS fingerprint canonical 模板（跨 edge 对齐）

现行约定见 [`docs/accounts/anthropic-oauth-edge-guidelines.md`](docs/accounts/anthropic-oauth-edge-guidelines.md)。

**反模式**：`enable_tls_fingerprint=true` 但 **`tls_fingerprint_profiles` 无对应模板行／账号无可靠 `tls_fingerprint_profile_id` 绑定** → 运行时会退回**内置默认** ClientHello，后台无法在模板表中点名在用参数；**不要用** **`tls_fingerprint_profile_id=-1`** 随机指纹跑生产 OAuth（库里每多一条模板，随机抽到其中一条的不确定性就上升）。

**标准要求**：每一条 **Anthropic、`type=oauth` 的边缘账号**，必须绑定 **`tls_fingerprint_profiles.name = tk_canonical_cc_oauth`**；字段体以 **`deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`** 的 `shared_baseline.tls_profile` 为单一真值源（对照：`deploy/aws/stage0/tk_canonical_cc_oauth.json`）。

**与本流水线的关系**：guard-drift force-template-rewrite（`generate_sql`）会 **`ON CONFLICT (name)` upsert** canonical profile，并把 `accounts.extra.tls_fingerprint_profile_id` 写成对应行 **`id`**；`check` / `verify` 比对 live `tls_profile.*` vs baseline **`tls_profile`** 块。**弃用手建并排模板**：**已废止名** **`claude_cli_nodejs24_fixed`** 不得再绑定新账号；库中无主账号绑定其 id 时，须在 Admin「TLS 指纹模板」删除该行。**删前**须确认无主账号 **`extra.tls_fingerprint_profile_id`** 仍指向该 id，否则运行时查找不到行→退回内置默认（silent 漂移）。

**admin UI**：`enable_tls_fingerprint` + 下拉选 canonical 名一致；尚无模板行时先跑一次 `remediate-guard-drift`，再绑定账号。

> ⚠️ TLS 模板的 upsert+绑定 SQL 历史上挂在写入面 (A) `edge_account_tier` apply 里（与 tier 数值同事务）。现在 tier **数值**由 admin UI ApplyTier 写、reconciler 自愈并发；本 skill 通过 **`plan-guard-drift-fix` / `remediate-guard-drift`** 只触发 force-template-rewrite 的那一段 SQL（重写 TLS profile + 绑定 + credentials 模板字段），**不**把 tier baseline 数值当作可调旋钮。

## 2. 流水线：snapshot → check →（TLS / UA）→ verify

每阶段一个命令，输入/输出明确，失败即停。所有写入通过 JSON 派生的 SQL（无静态 SQL 模板），operator 不写 SQL。

```bash
JOBDIR="$CLAUDE_JOB_DIR"               # or any scratch dir
MGR=ops/anthropic/manage-anthropic-config.py

# Stage 1 — Snapshot：拉所有 deployable edge 的 anthropic OAuth account + prod anthropic api-key stub 状态到 JSON
python3 $MGR snapshot --out $JOBDIR/snap.json

# Stage 2 — Check（联查）：每个 edge 跑 OAuth 稳定性 guard，读出 TLS / 余额 /
#   tier 表（vs git）/ HTTP UA + mimicry manifest / Redis 缓存 blob vs DB 漂移
python3 $MGR check --snapshot $JOBDIR/snap.json
#   退出码：0 全绿 / 1 violation（含 tls_profile drift、operator_balance 低于门槛、
#           tier_table_drift、http_ua_drift、redis_cache_drift 等）/ 2 error
#   注意：check 只“报告”漂移。修复入口分流：
#     - tls_profile 漂移 → 本 skill Stage 3（plan-guard-drift-fix / remediate-guard-drift）
#     - http_ua_drift（live settings.claude_code_user_agent_version / mimicry manifest
#       != deploy/aws/stage0/anthropic-http-mimicry-baselines.json）→ 本 skill Stage 4
#       `sync-runtime`。这是把「最关键的 HTTP 指纹配置」纳入 check 的覆盖面：cc bump 合并后
#       若忘记 sync-runtime，fleet live UA 会停在旧版本而 check 旧版**不会报**——现已会 violation。
#       **始终 live 读**（即使传 --snapshot）：UA 是部署级运行时旋钮，snapshot 不抓它。
#     - redis_cache_drift（live Redis blob `tls_fingerprint_profiles` / `tiers` vs 各节点
#       DB 权威表）→ DB 改了行但没失效 Redis 缓存（裸 SQL INSERT/UPDATE 或重构漏调
#       Invalidate/NotifyUpdate）会让 `ResolveTLSProfile` 服务 stale blob，运行时**静默
#       回退内置默认 ClientHello**（DB 对、运行时错）。guard 与旧 check 只读 DB，看不到
#       这条；现在 check 同时读 Redis blob + DB 表逐节点比对 count / (id,name) 集合 /
#       updated_at stale。冷缓存（key 缺失）**不**算漂移（读穿会从 DB 重建）。修复=对该节点
#       `DEL <key>` + `PUBLISH <key>_updated`（或重启）；**始终 live 读**，snapshot 不抓 Redis。
#     - operator_balance / pool_mode / concurrency 漂移 → 后端 reconciler 自愈（§3）
#     - tier_table_drift（live `tiers` 表 != git baseline）→ tier 行被 admin 后台改过
#       （PUT /admin/tiers/:id），下次重启/发版会被 ensureSeededFromBaseline 回刷；
#       要么撤销后台改动、要么把改动落进 git baseline JSON 再发版。
#   ⚠️ check **不再** diff 账号持久化 extra 的 8 个 tier-managed 键（base_rpm /
#      max_sessions / rpm_sticky_buffer / session_idle_timeout_minutes /
#      cache_ttl_override_*）：
#      PR #472 后这些值在 `tiers` 表、运行时 overlay 到账号、写路径剥离，账号 extra
#      为 null 是**正确态**。它们的正确性由 tier_table_drift（tier 表 vs git）保证，
#      不再按账号比对（旧逻辑对每个账号每次都假报，已重构）。

# Stage 3 — TLS 模板修复（仅当 check 报 /tls_profile/* 漂移或“启用 TLS 却无 profile”）
#   3a) 从 check 报告生成 force-template-rewrite 多 action plan：
python3 $MGR plan-guard-drift-fix \
  --snapshot $JOBDIR/snap.json \
  --check-report $JOBDIR/check.json \
  --out $JOBDIR/plan-guard-drift-fix.json
python3 $MGR apply  --plan $JOBDIR/plan-guard-drift-fix.json \
  --confirm yes-apply-anthropic-config-cascade --sync-runtime
python3 $MGR verify --plan $JOBDIR/plan-guard-drift-fix.json     # drift_count 必须=0
#   3b) 或一键：snapshot → check → plan → apply(--sync-runtime) → verify → check
#   默认 P0 加速：每 edge 1 次 SSM bundle snapshot + 1 次 batch guard；跨 edge 并行
#   （--parallel-edges N，默认 6）；apply/sync-runtime 按 instance 分组并行。
python3 $MGR remediate-guard-drift \
  --confirm yes-apply-anthropic-config-cascade \
  --job-dir $JOBDIR/remediate
# 回退旧路径（慢）：加 --legacy-guard，snapshot/guard 仍并行但 guard 恢复 1+N SSM/edge

# Stage 4 — HTTP UA / mimicry 运行时同步（settings + Redis 指纹缓存）
# UA semver + mimicry manifest 默认从 deploy/aws/stage0/anthropic-http-mimicry-baselines.json 解析
python3 $MGR sync-runtime --target prod --snapshot $JOBDIR/snap.json
python3 $MGR sync-runtime --target edge:uk1
python3 $MGR sync-runtime --target all-deployable-and-prod --snapshot $JOBDIR/snap.json
#   可选：先出审计 plan（不写库），核对 cc_version / 两个机型 manifest：
python3 $MGR plan-http-mimicry-sync --out $JOBDIR/plan-ua.json
```

`apply --sync-runtime` 在 DB 事务成功后，对 plan 中触及的 **edge + prod（默认）** 执行 `sync-runtime` 同一组动作：

1. `settings.claude_code_user_agent_version` UPSERT（semver 来自 `anthropic-http-mimicry-baselines.json` 的 `cc_version`）
2. `settings.claude_code_http_mimicry_manifest` UPSERT（`sonnet_opus` / `haiku` manifest）
3. `DEL fingerprint:{oauth_account_id}`（`env -u REDISCLI_AUTH` 避免容器空 AUTH 噪声）

prod 无 OAuth 账号时只写 settings；edge 两者都写。HTTP UA 运行时 self-heal 见 `docs/accounts/anthropic-oauth-edge-guidelines.md`；apply / TLS 模板变更后清 Redis 是为了立刻丢弃 stale HTTP 指纹缓存。

### 各阶段语义

| 阶段 | 输入 | 输出 | exit |
|---|---|---|---|
| snapshot | EC2/Lightsail SSM 权限 | `snap.json`：`edges.*.oauth_accounts` + `prod.anthropic_stubs`，**字段名嵌在值旁**（jsonb_agg） | 0 / 2 error |
| check | snap.json | 每 edge 跑 `check-edge-oauth-stability.py`（含 `tls_profile` diff）+ `operator_balance` + `tier_table_drift` + `http_ua_drift` + `redis_cache_drift`（**始终 live 读**：UA settings/mimicry manifest vs baseline JSON；Redis blob `tls_fingerprint_profiles`/`tiers` vs 各节点 DB 表）；**报告**，drift/error 计入 violation | 0 ok / 1 violation / 2 error |
| plan-guard-drift-fix | snap.json + check.json（或重跑 guard） | 每个 `status=drift` 账号一个 `edge_account_tier` action（force template rewrite，重写 TLS profile + 绑定 + credentials 模板字段） | 0 / 2 |
| remediate-guard-drift | confirm + job-dir | 上述全流程（snapshot → check → plan → apply --sync-runtime → verify → check）artifact 落盘 | 0 / 1 |
| apply | plan.json + confirm | 逐 action 渲染 SQL → SSM；可选 `--sync-runtime` 写 settings + 清 Redis | 0 / 1 step failed / 2 |
| plan-http-mimicry-sync | （读 baseline JSON） | `plan.json`：1 个 `kind=http_mimicry_runtime_sync` 审计 action（不写库，apply via sync-runtime） | 0 |
| sync-runtime | target + 可选 snapshot | settings UA + mimicry manifest upsert + Redis `fingerprint:{id}` DEL | 0 / 1 |
| verify | plan.json | 再 snapshot + 比对**每个** `actions[*].expected_after` vs live；drift 列表 | 0 / 1 drift / 2 |

### snapshot JSON 结构速查

解析 `snap.json` 别猜形状（`edges` 是**按 edge_id 索引的 dict**，不是 list；edge 账号在 **`oauth_accounts`**；prod stub 在 `prod.anthropic_stubs`，独立顶层 key）：

```jsonc
{
  "version": <int>, "captured_at": "...Z",
  "edges": {
    "us1": {                       // key = edge_id
      "deployable": true, "instance_id": "i-...", "region": "...",
      "oauth_accounts": [          // ← edge OAuth 账号在这里（check 比对 tier baseline + tls_profile）
        { "id": 1, "name": "...", "stability_tier": "l5",
          "base_rpm": 28, "rpm_sticky_buffer": 20, "concurrency": 10,
          "max_sessions": 100, "status": "active",
          "schedulable": true, ... }
      ]
    },
    "uk1": { "deployable": false, "skipped_reason": "planned; pass --allow-planned" }
  },
  "prod": {                        // ← 顶层；不嵌在 edges 里
    "instance_id": "i-...", "region": "us-east-1",
    "domain": "api.tokenkey.dev",
    "anthropic_stubs": [           // ← prod 全部 anthropic api-key 账号（check / sync-runtime 用）
      { "id": 42, "name": "cc-us1", "type": "apikey", "status": "active",
        "schedulable": true, "concurrency": 16,
        "cred_base_url": "https://api-us1.tokenkey.dev",
        "cred_pool_mode": true,
        "cred_pool_mode_retry_count": 1 }
    ]
  }
}
```

planned / 未快照的 edge 带 `skipped_reason`（或 `error`）且无 `oauth_accounts`——遍历时跳过它们。`prod.error` / `prod.skipped_reason` 同理。

> 上面 stub 里的 `cred_pool_mode` / `concurrency` 等字段如今由**后端 reconciler 自愈**（§3），本 skill 只读它们做 check 联查与 UA 同步，**不**再驱动它们的写入。

## 2.5 tier 值实时下发（check → apply-tiers-live → verify，不发版）

**场景**：tier baseline 改动**已合并进 git**（如 PR #604/#629 改 L1–L5 的 concurrency / caps），想**立即**铺到全 fleet，不等下次发版。`check` 会把这种「live 旧值 vs git 新值」报成 `tier_table_drift`（每节点每 tier）。此时用 **`apply-tiers-live`** 把 git 值实时下发。

**与 §3 的关系**：这**不是** §3 里那些废弃的 `plan-tier-bump` 等 escape hatch。tier **数值**仍以 git baseline JSON 为单一源；`apply-tiers-live` 只是把**已在 git 的值**确定性地铺到运行库，落库状态与 admin UI ApplyTier 完全一致，且与下次发版幂等收敛（embed == 同一份 JSON，由 `check-tier-baseline-embed.py` 钉死）。reconciler 仍对单账号 tier 字段漂移 report-only——本工具是 operator 显式触发的 fleet 下发，不抢 reconciler 的活。

**硬约束（顺序不能错）**：

1. **新值必须先合并进 git**。运行时 8 个 cap 从本机 `tiers` 表 overlay（账号 extra 为 null，#472），`apply-tiers-live` 读**磁盘上的 git JSON**。若推未提交的工作区值，下次发版 `ensureSeededFromBaseline` 用 embed(=已合并值)盖回 → live 改动丢失。**tier 值改动走独立小 PR 合并后再 apply。**
2. **直连 SQL over SSM，复刻后端三个副作用**（与 admin handler 一致）：① `tiers` 表 UPDATE（11 strategy 列，**不碰** tls_profile）+ Redis `DEL tiers`/`PUBLISH tiers_updated refresh`（= `TierService.invalidateAndNotify`）；② `accounts.concurrency` UPDATE + `scheduler_outbox('account_changed')` INSERT（worker 1s 内 re-read DB 重建快照）；③ **operator(users.id=1) Σ 同步**（同事务，= sanctioned tier-apply 注入的 `render_admin_operator_concurrency_sync_sql`，立即对齐不靠 reconciler 时序）。concurrency 不在 tier_table_drift 比对键（只比 8 cap），故 verify 单独断言 tiers/账号 concurrency==git。
3. **不用 admin-API**：运行镜像无 curl（busybox wget 不能 PUT）、admin 鉴权未 bootstrap；psql+redis-cli 镜像里就有且本脚本族已在用。

**一条龙命令**：

```bash
JOBDIR="$CLAUDE_JOB_DIR"
MGR=ops/anthropic/manage-anthropic-config.py

# 0) 先确认 git main 已含目标 tier 值（独立 PR 已合并）
python3 -c "import importlib.util as u;s=u.spec_from_file_location('m','$MGR');m=u.module_from_spec(s);s.loader.exec_module(m);print(m._load_expected_tiers()['l5'])"

# 1) check：报 tier_table_drift（live 旧 vs git 新）
python3 $MGR snapshot --out $JOBDIR/snap-pre.json
python3 $MGR check --snapshot $JOBDIR/snap-pre.json        # exit 1 + tier_table_drift items

# 2) apply：实时下发 git 值到全 deployable edge + prod
python3 $MGR apply-tiers-live --snapshot $JOBDIR/snap-pre.json \
  --confirm yes-apply-tiers-live-from-git --job-dir $JOBDIR --json
#   选项：--edges uk1,us7  限定范围；--skip-prod；--no-concurrency 只改 tiers 表+缓存

# 3) verify：必须传【重新抓的】snapshot（pre 的反映不了 apply 后状态）
python3 $MGR snapshot --out $JOBDIR/snap-post.json
python3 $MGR apply-tiers-live --snapshot $JOBDIR/snap-post.json --verify-only --json
#   期望 clean=True：tier_table_drift==0 且 tiers/accounts concurrency==git
python3 $MGR check --snapshot $JOBDIR/snap-post.json        # 独立第三方确认：any_violation=False + redis_cache_drift 干净
```

**确认码**：`--confirm yes-apply-tiers-live-from-git`（区别于级联 `CONFIRM_CODE`，防误粘）。

**残留风险**：live 写、非发版。值扛过 tier 缓存 pub/sub、scheduler outbox、5min 全量 rebuild；唯一回滚点是**下次发版前某节点在旧镜像上重启**（tiers 行被旧 embed reseed 回去；`accounts.concurrency` 列不被 tier seeding 重写，扛过重启）。不静默——下次 `check` 重报 `tier_table_drift`。**根治 = 下次例行发版**（embed==git，幂等 reseed 同值，重启 durable）。本工具是「立即生效」桥，发版是「永久固化」。

## 3. 已下沉到后端（不再由本 skill 驱动）

这些能力曾是本 skill 的写入面 (A) tier 数值 / (B) / (C) / (D) / (E)；在 `origin/main` 上已下沉到**后端 per-node 自愈 reconciler + admin UI**，本 skill **不再编排其写入**。改这些值请走对应入口，不要再用 `plan-tier-bump` / `plan-stub-pool` / `plan-concurrency-mirror` / `plan-group-claude-code-only` / `plan-edge-operator-balance`（脚本里仍保留这些子命令作 fleet 级紧急 escape hatch，但不再是推荐路径）。

> **注意区分**：§2.5 的 `apply-tiers-live` **不在**上面这串废弃命令里。废弃的 `plan-tier-bump` 写**账号 extra** 的 tier-managed 键——#472 后那些键运行时从 tiers 表 overlay、账号侧已剥离，所以 plan-tier-bump 对 cap 是 **inert**（写进去会被 strip）。`apply-tiers-live` 写的是**正确的面**：`tiers` 表（cap 的真正 overlay 源）+ `accounts.concurrency`（账号列）+ 缓存失效 + operator Σ，是把**已合并 git 值实时下发**的推荐路径。tier **数值的单一源**仍是 git baseline JSON / admin UI ApplyTier（per-account）——`apply-tiers-live` 不另起事实源，只做 fleet 下发。

| 旧写入面 | 现在谁负责 | 入口 |
|---|---|---|
| (A) tier baseline **数值**（concurrency / base_rpm / sticky_buffer / max_sessions / priority / stability_tier） | **admin UI `ApplyTier`** 显式设定；reconciler 对单账号 tier 字段漂移 **report-only（slog.Warn），永不静默重写** | admin UI 账号卡「Apply Tier」；后端 `account_handler_tk_tier.go` + `tier_service.go` |
| (A) `users.id=1` operator 并发 Σ（= Σ schedulable=true anthropic concurrency） | **reconciler Step A**（per-node 自愈）+ admin 控制面 `SumConcurrencyAnthropic` | `anthropic_config_reconciler.go` / `anthropic_operator_concurrency.go` |
| (B) prod 镜像 stub `credentials.pool_mode` + `pool_mode_retry_count` | **reconciler Step B**（自愈匹配 stub 的 pool_mode） | `anthropic_config_reconciler.go` |
| (C) prod stub concurrency 镜像（四跳级联 Σ schedulable） | **reconciler Step C**（自愈；失败/超时/5xx edge 读取**绝不写 0**，跳过保留旧值） | `anthropic_config_reconciler.go` |
| (D) anthropic group `claude_code_only` | **admin UI group 编辑**（`claude_code_only` 字段） | admin UI 分组；后端 `group_handler.go` |
| (E) edge `users.id=1.balance` 低于门槛（<100 → 9999999） | **reconciler Step E**（edge 部署自愈余额下限 + 失效 Redis `billing:balance:1`）。⚠️ **部署 gate**：受 `GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_BALANCE_FLOOR_ENABLED` 控制——代码默认 **false**，compose 透传默认 false（prod 的 users.id=1 余额是真实计费面，**必须**保持 false），仅 edge bootstrap 写入的 `.env` 置 true（`render-bootstrap.sh`）。在该 env 接线落地**之前** provision 的存量 edge 容器内无此变量 → Step E 不跑，余额漂移只能靠 escape hatch `plan-edge-operator-balance` 手动注资（2026-06-08 uk2/uk3 即此案例） | `anthropic_config_reconciler.go`（常量 `anthropicEdgeBalanceFloor*`，对照 `anthropic-edge-operator-balance-baselines.json`）；gate 接线：`deploy/aws/stage0/docker-compose.yml` + `deploy/aws/lightsail/render-bootstrap.sh` |

> reconciler 边界（取自其文件头）：**只写本机库、绝不冒充 fleet**；safe items（operator Σ / pool_mode / 余额下限 / surface-C 并发镜像）自愈；**单账号 tier 字段漂移只报告**，因为 tier 经 admin UI ApplyTier 显式设定。fleet 级 fan-out 仍留在 Python pipeline（紧急时用，见上）。
>
> `group.rpm_limit` 一直**不**由任何自动流水线写——admin UI 独立设置，与 account 字段解耦。

## 4. 不在本流水线范围内（独立操作 / admin UI）

| 配置面 | 写入方式 |
|---|---|
| edge / prod `group.rpm_limit` | admin UI 直接编辑；operator 凭运维经验定独立绝对值，与 account 字段解耦 |
| edge / prod 其他 `group` 字段（name / fallback / model_routing / `claude_code_only` …） | admin UI |
| account tier 数值（见 §3 (A)） | admin UI `ApplyTier` |
| prod anthropic apikey stub 的 `base_url` / `api_key` / 名字等个体字段 | admin UI（pool_mode / concurrency 由 reconciler 自愈，见 §3） |
| edge OAuth `account_groups` 绑定 | admin UI |
| prod anthropic stub `account_groups` 双绑（default + cc-edges，见 §5） | admin UI |
| OAuth 凭据轮换 / status | admin UI / OAuth flow |

## 5. prod 控制面：anthropic stub 双绑规则

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

## 6. 故障速查

| 现象 | 处理 |
|---|---|
| snapshot 失败 / SSM 拒绝（edge 或 prod） | 校验实例在跑（EC2 CFN 或 Lightsail Hybrid `mi-*`）/ 双矩阵 domain 或 `PROD_TARGET` / OIDC 权限。**仅排障 edge** 跑 `snapshot --skip-prod` 临时绕开 prod 失败 |
| `apply --confirm` 拒绝 | 必须精确 `yes-apply-anthropic-config-cascade` |
| `tls_profile` drift（`/tls_profile/...` 或 UK 模式：启用 TLS 却无 profile） | 用 **`plan-guard-drift-fix`** 或 **`remediate-guard-drift`**（含 `apply --sync-runtime`）force-template-rewrite，不要手工拼 SQL |
| check guard 报 `status: drift` 且 `diffs[].path` 含 `/credentials/temp_unschedulable_rules`，但数值字段全等 | guard-drift force-template-rewrite 会重写 credentials 端字段；apply 完跑一次 `check` 当真值 |
| check guard 对账号 `extra.base_rpm` / `max_sessions` 等报 drift | **不应再发生**：PR #472 后这 8 个 tier-managed 键由 `tiers` 表 overlay、账号侧不持久化，guard 已停止比对它们（旧逻辑假报）。若仍看到，说明 guard 未更新——核对 `check-edge-oauth-stability.py` 的 `TIER_MANAGED_EXTRA_KEYS` 排除逻辑 |
| check 报 `tier_table_drift`（live `tiers` 表 != git baseline，violation/exit 1） | 三种成因分流：(a) **git baseline 刚改过**（新值已合并、live 还是旧镜像的旧值）→ 想立即生效不等发版用 **§2.5 `apply-tiers-live`**，否则等下次例行发版 `ensureSeededFromBaseline` reseed；(b) tier 行被 **admin 后台**改过（`PUT /admin/tiers/:id`，全副本即时生效但下次重启/发版被回刷）→ 若误改就 admin UI 改回，若有意就**把新值落进 git baseline JSON**（`deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json` + 同步 embed/迁移，过 `check-tier-baseline-embed.py`）后再 `apply-tiers-live` / 发版固化。看 `items[].warning` 定位 node/tier/字段 |
| check 报账号 `account_field_drift`（非 tier-managed 字段，如 priority / concurrency） | concurrency 由 reconciler 自愈（§3）；其余账号级字段走 admin UI |
| check 报 `operator_balance` 低于门槛 / pool_mode / concurrency 漂移 | 由后端 reconciler 自愈（§3）；若持续未自愈，**先查该 edge 容器内 `GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_BALANCE_FLOOR_ENABLED` 是否=true**（balance-floor gate 默认 false，仅 edge bootstrap `.env` 置 true；接线前 provision 的存量 edge 没有它——见 §3 (E) 行），再查 reconciler leader 锁 / slog 日志。容器无 gate 时用 escape hatch：`plan-edge-operator-balance --snapshot snap.json` → `apply --confirm` → `verify`（只会写 violation 的 edge，healthy edge skipped_ok） |
| check 报 `http_ua_drift` / HTTP UA 未生效 | `sync-runtime --target …`（或先 `plan-http-mimicry-sync` 核对 manifest）；确认 `anthropic-http-mimicry-baselines.json` 的 `cc_version` 已是目标版本。典型成因：cc 版本 bump PR 合并后忘了跑 sync-runtime，fleet live UA 仍停在旧版本——check 现在会 violation（exit 1），不再假绿 |
| check 报 `redis_cache_drift`（live Redis blob != 该节点 DB 表，violation/exit 1） | DB 行被改但没失效 Redis 缓存（裸 SQL INSERT/UPDATE，或重构漏调 `Invalidate()`/`NotifyUpdate()`）→ `ResolveTLSProfile` 服务 stale blob，运行时**静默回退内置默认 ClientHello**。看 `items[].warning` 定位 `node` + `cache`（`tls_fingerprint_profiles`/`tiers`）+ 漂移类型（`key-set` 缺/多、`:name`、`:updated_at` STALE）。止血：对该节点 `DEL <cache>` + `PUBLISH <cache>_updated`（如 `DEL tls_fingerprint_profiles && PUBLISH tls_fingerprint_profiles_updated`；同时 DEL 有空窗，再 PUBLISH 一次零中断）或重启容器；根治走 `remediate-guard-drift`（写 DB 时带失效）。`status=error`=该节点 SSM/解析失败，非干净判定，重跑或查节点。冷缓存（key 缺失）不报 |
| OAuth account `status=error/suspended` | OAuth 凭据问题（token 过期 / 403 / 上游禁用），见 OAuth 故障文档；不在本流水线范围 |
| verify drift | operator 决定再 apply 或回滚（用 admin 前端按 plan.live_inputs.* 的 before 反向写回） |

## 7. 附录：baseline JSON 速查与 stable accounts

- TLS canonical 模板真值源：`deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json` 的 `shared_baseline.tls_profile.name` **必须为** `tk_canonical_cc_oauth`（对照 `deploy/aws/stage0/tk_canonical_cc_oauth.json`）；guard-drift `generate_sql` 据此 upsert profile + 绑定 `tls_fingerprint_profile_id`。
- HTTP UA / mimicry 真值源：`deploy/aws/stage0/anthropic-http-mimicry-baselines.json`（`cc_version` + `sonnet_opus` + `haiku`）；`sync-runtime` / `plan-http-mimicry-sync` 读它。
- tier baseline 数值仍存在于同一份 tiered JSON（`tier_order: l1..l5` 的 `baseline.account.*` / `baseline.extra.*`），但现由 **admin UI ApplyTier** 写、reconciler 自愈并发——本 skill 只在 check 比对时读，不写。
- 更新 stable_accounts 列表（仅人工确认后）：
  ```bash
  python3 ops/anthropic/check-edge-oauth-stability.py \
    --edge-id $EDGE --account-name $ACCT \
    --update-stable-list --confirm yes-update-anthropic-stable-list
  ```
  禁止在未确认稳定前更新 stable list。

## 8. 附录：底层工具（emergency / debug）

正常流程**只走上面的命令**。下列工具在流水线 break 或紧急 rollback 时直接用：

- `ops/anthropic/check-edge-oauth-stability.py --edge-id E --account-name A [--json] [--emit-sql FILE]` — 单 edge OAuth tier baseline / TLS drift 只读检查；`--emit-sql` 按账号 live tier 渲染 TLS profile upsert + 绑定 SQL（再 base64 经 SSM 注入）。

**底线**：手动绕开 orchestrator 时 op 必须自己做 apply 后复核——同样不允许跳过 §0 "先查后说"协议。

### 8.1 OAuth 账号 email 机队审计（probe-account-emails.sh）

CC gateway userEmail 回填读 `accounts.extra` / `credentials` 的 email 字段。缺 email 时 normalize 会**删除** client userEmail 行而非替换。

```bash
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-account-emails.sh
```

输出：`tk_anthropic_request_normalize_enabled` 设置 + 全账号 resolved_email + schedulable OAuth 缺 email 计数。

## 9. 扩展阅读

- `ops/anthropic/manage-anthropic-config.py`（orchestrator；本 skill 用其 `snapshot` / `check` / `plan-guard-drift-fix` / `remediate-guard-drift` / `apply` / `sync-runtime` / `plan-http-mimicry-sync` / `verify` / `apply-tiers-live`）
- `backend/internal/service/tier_service.go`（`ensureSeededFromBaseline` UpsertByName + `invalidateAndNotify`：§2.5 `apply-tiers-live` 复刻的缓存失效真值源）
- `backend/internal/service/anthropic_config_reconciler.go`（**§3 写入面 A 并发 / B / C / E 的 per-node 自愈真值源**；文件头列出 boundary 与 step 顺序）
- `backend/internal/handler/admin/account_handler_tk_tier.go` + `backend/internal/service/tier_service.go`（admin UI `ApplyTier`：tier 数值的写入入口）
- `backend/internal/handler/admin/group_handler.go`（group `claude_code_only` 写入入口）
- `backend/internal/service/anthropic_operator_concurrency.go`（控制面与 reconciler 共享的 Σ schedulable→`users.id=1` 语义）
- `ops/anthropic/check-edge-oauth-stability.py`（`generate_sql`：`tls_profile` upsert + `extra.tls_fingerprint_profile_id` 绑定）
- `docs/accounts/anthropic-oauth-edge-guidelines.md`（OAuth edge TLS + UA 现行约定短文）
- `deploy/aws/stage0/tk_canonical_cc_oauth.json`（canonical TLS profile JSON，与 tiered baseline `shared_baseline.tls_profile` 对齐）
- `deploy/aws/stage0/anthropic-http-mimicry-baselines.json`（HTTP UA / mimicry manifest 唯一真值源）
- `ops/observability/probe-account-emails.sh`（fleet OAuth email + normalize 开关只读审计；§8.1）
