---
name: tokenkey-anthropic-oauth-config
description: >-
  TokenKey Anthropic OAuth read/check/remediation workflow. Use for manage-anthropic-config snapshot/check/sync-runtime, tk_canonical_cc_oauth TLS template drift, Claude Code UA/http mimicry sync, or OAuth stability guard checks across prod/edges.
---

# TokenKey：Anthropic OAuth 配置流水线（check 联查 + TLS 模板 + HTTP UA）

适用于 TokenKey Anthropic OAuth 的只读 snapshot/check、TLS/HTTP 指纹同步、已合并 tier 值实时下发和账号 email 审计/补全。执行 owner：`ops/anthropic/manage-anthropic-config.py`；email 操作用 `ops/observability/run-probe.sh` 投递现有 probe。按下面操作条件读取参考文件，不全量加载。

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

仅诊断/修复 TLS profile 时读取。Anthropic OAuth 必须绑定 `tk_canonical_cc_oauth`；不使用 `-1` 随机指纹。 见 [操作细则](references/tls-profile.md)。

## 2. 流水线：snapshot → check →（TLS / UA）→ verify

默认只读：`python3 ops/anthropic/manage-anthropic-config.py snapshot --out "$CLAUDE_JOB_DIR/snap.json"` → `check --snapshot "$CLAUDE_JOB_DIR/snap.json"`。check 的 0/1/2 表示通过/漂移/错误；UA/Redis 始终 live 读。只有处理 TLS/UA 漂移、解析 snapshot 或执行 plan/apply/verify 时展开细则。写入须在用户授权范围内；apply 任一步失败停止，verify 必须跑。 见 [操作细则](references/pipeline.md)。

## 2.5 tier 值实时下发（check → apply-tiers-live → verify，不发版）

仅将**已合并** git tier 值实时下发时读取；写 `tiers` 表、并发列及缓存/outbox，不写账号 extra 的 tier-managed 键。写前读完整步骤，写后重抓 snapshot 验证；旧镜像重启可能用旧 embed 回刷。 见 [操作细则](references/tiers-live.md)。

## 3. 已下沉到后端（不再由本 skill 驱动）

operator Σ、stub pool/concurrency、edge 余额由后端 reconciler 负责；单账号 tier 漂移只报告，走 Admin ApplyTier。禁止默认使用旧 `plan-tier-bump` 等 escape hatch；prod 余额自愈 gate 必须保持 false。仅排查这些 owner 的自愈或紧急接管时读取。 见 [操作细则](references/backend-owners.md)。

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

check/SSM/apply 失败时按具体报错读取；不要从一次失败扩大写入范围。 见 [操作细则](references/troubleshooting.md)。

## 7. 附录：baseline JSON 速查与 stable accounts

仅查 baseline 文件/字段归属时读取；值由 JSON owner 提供，不凭记忆推断。 见 [操作细则](references/baselines.md)。

## 8. 附录：底层工具（emergency / debug）

账号 email 审计/补全，或需要底层 emergency/debug 时读取。审计用 `probe-account-emails.sh`；写 email 用 `apply-account-contact-email.sh`，须在用户授权范围内。 见 [操作细则](references/emergency.md)。

## 9. 扩展阅读

仅需要定位实现、契约或 probe owner 时查询。 见 [操作细则](references/owners.md)。
