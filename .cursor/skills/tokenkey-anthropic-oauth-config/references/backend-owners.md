## 3. 已下沉到后端（不再由本 skill 驱动）

这些能力曾是本 skill 的写入面 (A) tier 数值 / (B) / (C) / (D)；在 `origin/main` 上已下沉到**后端 per-node 自愈 reconciler + admin UI**，本 skill **不再编排其写入**。改这些值请走对应入口，不要再用 `plan-tier-bump` / `plan-stub-pool` / `plan-concurrency-mirror` / `plan-edge-operator-balance`（脚本里仍保留这些子命令作 fleet 级紧急 escape hatch，但不再是推荐路径）。旧的 `plan-group-claude-code-only` / `anthropic-group-claude-code-baselines.json` 已删除，避免把对外默认 group 重新强制成 Claude-Code-only。

> **注意区分**：§2.5 的 `apply-tiers-live` **不在**上面这串废弃命令里。废弃的 `plan-tier-bump` 写**账号 extra** 的 tier-managed 键——#472 后那些键运行时从 tiers 表 overlay、账号侧已剥离，所以 plan-tier-bump 对 cap 是 **inert**（写进去会被 strip）。`apply-tiers-live` 写的是**正确的面**：`tiers` 表（cap 的真正 overlay 源）+ `accounts.concurrency`（账号列）+ 缓存失效 + operator Σ，是把**已合并 git 值实时下发**的推荐路径。tier **数值的单一源**仍是 git baseline JSON / admin UI ApplyTier（per-account）——`apply-tiers-live` 不另起事实源，只做 fleet 下发。

| 旧写入面 | 现在谁负责 | 入口 |
|---|---|---|
| (A) tier baseline **数值**（concurrency / base_rpm / sticky_buffer / max_sessions / priority / stability_tier） | **admin UI `ApplyTier`** 显式设定；reconciler 对单账号 tier 字段漂移 **report-only（slog.Warn），永不静默重写** | admin UI 账号卡「Apply Tier」；后端 `account_handler_tk_tier.go` + `tier_service.go` |
| (A) `users.id=1` operator 并发 Σ（= Σ schedulable=true anthropic concurrency） | **reconciler Step A**（per-node 自愈）+ admin 控制面 `SumConcurrencyAnthropic` | `anthropic_config_reconciler.go` / `anthropic_operator_concurrency.go` |
| (B) prod 镜像 stub `credentials.pool_mode` + `pool_mode_retry_count` | **reconciler Step B**（自愈匹配 stub 的 pool_mode） | `anthropic_config_reconciler.go` |
| (C) prod stub concurrency 镜像（四跳级联 Σ schedulable） | **reconciler Step C**（自愈；失败/超时/5xx edge 读取**绝不写 0**，跳过保留旧值） | `anthropic_config_reconciler.go` |
| (D) edge `users.id=1.balance` 低于门槛（<100 → 9999999） | **reconciler Step E**（edge 部署自愈余额下限 + 失效 Redis `billing:balance:1`）。⚠️ **部署 gate**：受 `GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_BALANCE_FLOOR_ENABLED` 控制——代码默认 **false**，compose 透传默认 false（prod 的 users.id=1 余额是真实计费面，**必须**保持 false），仅 edge bootstrap 写入的 `.env` 置 true（`render-bootstrap.sh`）。在该 env 接线落地**之前** provision 的存量 edge 容器内无此变量 → Step E 不跑，余额漂移只能靠 escape hatch `plan-edge-operator-balance` 手动注资（2026-06-08 uk2/uk3 即此案例） | `anthropic_config_reconciler.go`（常量 `anthropicEdgeBalanceFloor*`，对照 `anthropic-edge-operator-balance-baselines.json`）；gate 接线：`deploy/aws/stage0/docker-compose.yml` + `deploy/aws/lightsail/render-bootstrap.sh` |

> reconciler 边界（取自其文件头）：**只写本机库、绝不冒充 fleet**；safe items（operator Σ / pool_mode / 余额下限 / surface-C 并发镜像）自愈；**单账号 tier 字段漂移只报告**，因为 tier 经 admin UI ApplyTier 显式设定。`groups.claude_code_only` 不属于 reconciler 或 ops check 自愈面。fleet 级 fan-out 仍留在 Python pipeline（紧急时用，见上）。
>
> `group.rpm_limit` 一直**不**由任何自动流水线写——admin UI 独立设置，与 account 字段解耦。
