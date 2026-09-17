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
