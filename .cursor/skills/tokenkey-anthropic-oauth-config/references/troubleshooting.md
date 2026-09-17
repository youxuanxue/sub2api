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
