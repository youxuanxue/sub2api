# 同机蓝绿零停机发版 — 触发式 Backlog（已封存，当前不实施）

> **状态：封存（DEFERRED）。** 2026-06 决策（100 用户阶段）：**现在不做蓝绿。**
> 本文件是「抽屉里的完整方案」——满足 §1 任一触发阈值即取出执行，设计已验证、可直接落地。
> 配套：常规发版现状见 `deploy/aws/README.md` §升级；灾难恢复见 `deploy/aws/RUNBOOK-disaster-recovery.md`；
> 真空实测工具 `ops/stage0/measure_deploy_blackout.sh`。

## 为什么封存（聚焦）

prod 单节点发版**已经近乎无缝**：`ops/stage0/deploy_via_ssm.sh` 做了预拉镜像（停机窗口外）+ SIGUSR1 优雅排空 + Caddy 主动摘除 + `lb_try_duration 30s` 排队重试 + 失败自动回滚。正常发版的客户端真空只有 old→new 切换的极短一刻，且这一刻是**排队而非拒绝**，API 客户端普遍自带重试。

蓝绿用「2 秒 → 0 秒」换来的是**永久的运维/认知复杂度**：两套 compose、swap、`active-color` 状态文件、cutover 一次性脚本、迁移必须 expand-contract 的门禁、秒级回退脚本——从此每次发版、每个 DB 迁移都要先在脑子里过一遍蓝绿。100 用户阶段，这是用工程优雅满足完美主义，不是给用户创造价值。

**真正会长时间停服的不是正常发版**，是「发版失败要排障」（deploy 脚本自动回滚已覆盖大半）和「整机/AZ 挂」（不在当前可用性目标内，且已有 `RUNBOOK-disaster-recovery.md` 冷重建兜底）。蓝绿优化的恰恰是最不痛的点。

## 1. 触发阈值（满足任一即重启实施）

- **① 商业承诺**：出现付费用户，或对外承诺 SLA（如 99.9%）。
- **② 实测真空可见**：`measure_deploy_blackout.sh` 对真实发版实测，最长真空导致客户端可见失败（5xx 透传、连接拒绝，且客户端重试无法覆盖）。**这是最硬的客观信号——先用数据说话。**
- **③ 发版频率**：发版频繁到每次 2 秒真空累积成可见客诉（如日均多次发版）。
- **④ 规模增长**：活跃用户/并发显著上升（量级参考 >1k 活跃），瞬时真空波及面变大。

### 实测基线（待 `measure_deploy_blackout.sh` 跑出后填入）

| 日期 | 版本 | 最长真空 ms | 失败探针数 | 客户端是否可见 | 结论 |
|---|---|---|---|---|---|
| _待填_ | | | | | _无感 → 继续封存 / 可见 → 触发阈值②_ |

采集方法：发版时从干净 vantage 后台跑
`TOKENKEY_BASE_URL=https://api.tokenkey.dev DURATION_SECONDS=300 bash ops/stage0/measure_deploy_blackout.sh`，
同时 dispatch `deploy-stage0.yml`，把汇总行填进上表。

#### 附：本地实测参考（2026-06-04，非 prod 权威）

本地 stage0 栈（prod 同款 Caddy：active health 5s + `lb_try_duration 30s` 排队 + SIGUSR1 drain 联动；GHCR `:latest`；单容器，与 prod 单节点同构）实测一次 force-recreate 硬切：

| 维度 | 值 |
|---|---|
| 物理恢复（Caddy `/health` 重新 200） | ~10s |
| 客户端可见真空（1s 卡顿阈值） | ~5.9s |
| 失败率 | 6/287 ≈ 2%（全 000 连接拒绝，无 5xx） |
| 其余 | 281 请求被 `lb_try_duration` 排队后成功（2xx） |

**为何不直接当 prod 基线（caveat）**：
1. 这是**硬切**（force-recreate 无 drain）上界。真实发版走 SIGUSR1 drain（本地已验证 `SIGUSR1 received; drain mode activated` 生效、`/health`→503），但**单容器 drain 后无备用容器**，真空主导仍是新容器启动时长——这恰恰说明单节点真空的根源是「重启期间无备用」，正是蓝绿要消除的。
2. **`/health/inflight`、`/health/live` 曾被 SPA fallback 遮蔽（返回 HTML 而非 JSON）**，致 `ops/stage0/deploy_via_ssm.sh` 的 `in_flight=0` 等待空转 ~76s。根因 = `shouldBypassEmbeddedFrontend`（`backend/internal/web/embed_on.go`）的 bypass 列表漏了 `/health/` 前缀，**已由 #562（合并 `8a04abf5`）修复**。故上表硬切实测虽跳过了 drain（端点坏、空转无意义），但 #562 后真实发版的 drain 不再空转，prod 真空应回落到「新容器物理恢复 + Caddy 30s 排队」量级——下次发版用路径 B 实测即可同时验证 #562 效果与拿到生产基线。
3. 数字定义于「1s 无响应即真空」；客户端若容忍 30s 排队则真实失败更少。
4. **修正**：早先「~2s 真空」估计偏乐观，硬切实测约 **6s**。

**结论**：仍是小窗口（~6s）+ 低失败率（~2%）+ Caddy 30s 排队兜底，客户端有重试基本无感——印证「100 用户阶段不做蓝绿」。prod 权威值仍需路径 B。

---

## 2. 核心机制（已验证）

Caddy 同时配两个 upstream `tokenkey-blue:8080` / `tokenkey-green:8080`，靠 active health `/health` 自动路由到健康颜色，**复用现有 SIGUSR1 drain 机制**做切换。为省内存（t4g.small 2GB），平时只跑一个颜色，发版时临时起另一个颜色：

1. 读 `active-color`（`/var/lib/tokenkey/active-color`，blue/green），target = 另一色；
2. 给 target 设新镜像 tag，`compose up -d <target>`，等它 `/health=200` healthy；
3. 此刻两色都健康、Caddy 两 upstream 都可转发（短暂双版本并存，无状态共享同一 PG/Redis）；
4. SIGUSR1 drain 旧色 → `/health=503` → Caddy 自动只剩新色；等 `in_flight=0`；
5. 停旧色容器；写 `active-color=target`。
   **失败时**（target 没 healthy）：旧色从未被 drain/切走，零影响，停掉失败 target 即可。

### 已验证的技术结论

- **Caddy 容忍未起的 upstream**：静态主机名在 config-load 时不做 DNS 解析，缺失颜色在运行时被 active health 标 unhealthy 跳过，**不阻断配置加载**——所以「平时单色」可行，不必双色常驻。（参考 Caddy reverse_proxy 文档 + issue #3459）
- **drain 是进程级、无同名容器假设**：`drainFlag` 是每进程独立的 `atomic.Bool`；`docker kill -s USR1 tokenkey-blue` 只 drain blue 进程。`/health`（drain-aware→503）/ `/health/live`（恒 200，docker healthcheck 用）/ `/health/inflight`（loopback-only，脚本须 `docker exec <color> wget localhost:8080/...`）。无 `SetDrain(false)` 调用点 → 新色必须是全新 `up`（drain=false 初始），换色天然清 drain，**不需要单色模式那条 `--force-recreate` load-bearing 逻辑**。
- **内存**：t4g.small 2GB，发版重叠窗口双色并存峰值约 0.8–1.4 GB，**通常够但无安全垫**。缓解：给 prod CFN 加 `SwapSizeGiB`（抄 edge-ec2 的 swapfile 块）+ 给两色设 `mem_limit` + `GOMEMLIMIT` 钉峰值。升 t4g.medium 是最后手段。（实施前确认 prod single-ec2 当前是否已有 swap——edge 默认有 2GB，prod 待核。）
- **edge 不上蓝绿**：edge 是 t4g.micro（1GB），双色即便有 swap 也重度换页；edge 是薄 relay 节点、发版影响面小，维持现状单色 drain-recreate。**方案按节点 opt-in：prod 蓝绿，edge 现状。**

---

## 3. 文件改动清单（实施时）

> 下表标「新增」的脚本均位于 `ops/stage0/`（实施时创建；此处用裸文件名，因为它们尚不存在，
> 写全路径会被 `scripts/checks/script-ref-existence.py` 误判为悬空引用）。

| 文件 | 改动 | 关键点 |
|---|---|---|
| `deploy/aws/stage0/docker-compose.yml` | 引入 `x-tokenkey-app` YAML anchor，`tokenkey` 拆成 `tokenkey-blue` + `tokenkey-green`（各自 `container_name` + `TOKENKEY_IMAGE_{BLUE,GREEN}`）；加 `mem_limit`/`GOMEMLIMIT`；caddy `depends_on` 收敛到只依赖 postgres/redis | **此文件被 prod+edge 共享 embed，必须先拆 edge** |
| `deploy/aws/stage0/docker-compose.edge.yml`（新增） | edge 保留单色 `container_name: tokenkey`（=现状），与 prod 解耦 | 解决共享耦合 |
| `deploy/aws/stage0/Caddyfile` | `reverse_proxy tokenkey:8080` → `reverse_proxy tokenkey-blue:8080 tokenkey-green:8080`，其余不变 | 双 upstream + active health 自动路由 |
| `deploy/aws/stage0/Caddyfile.edge` | **不改**（edge 单色 `tokenkey:8080`） | edge opt-out |
| `deploy/aws/stage0/build-cfn.sh` | 拆 `COMPOSE_SRC` 为 prod / edge 两个 blob，分别 embed 进 single-ec2 / edge-ec2（仿 Caddyfile 已有的 prod/edge 分流），新增 `EDGE_COMPOSE_GZB64_SSM` marker | prod/edge compose 解耦 |
| `deploy/aws/cloudformation/stage0-single-ec2.yaml` | 加 `SwapSizeGiB`（抄 edge swapfile UserData）；`COMPOSE_GZB64_SSM` 指向蓝绿 compose；UserData 首启写 `active-color=blue` 并只 `up` active 色 | prod 加 swap + 蓝绿首启 |
| `deploy/aws/cloudformation/stage0-edge-ec2.yaml` | `EDGE_COMPOSE_GZB64_SSM` 指向单色 compose（功能等价现状） | edge 保持单色 |
| `sync_compose_via_ssm.sh`（新增） | 热同步 compose 到 live host（类比 `ops/stage0/sync_caddyfile_via_ssm.sh`，但无 envsubst、可直接 `gunzip >` 覆盖，只落盘 + `compose config -q` 校验、不 `up`） | 现存实例落地 |
| `deploy_via_ssm_bluegreen.sh`（新增） | 蓝绿切换原语（§2 序列）；rollback trap 大幅简化：失败发生在 drain 旧色之前，旧色未动，只需停掉失败 target，不恢复 | prod 发版 |
| `cutover_to_bluegreen_via_ssm.sh`（新增） | 一次性 cutover：单色 `tokenkey` → 蓝绿（见 §4） | 仅跑一次 |
| `check_migration_bluegreen_safe.sh`（新增） | 对本次新增迁移静态扫 `DROP COLUMN`/`DROP TABLE`/`SET NOT NULL`/`RENAME`/`ALTER...TYPE`，命中需迁移头注释显式标记才放行（CI gate，仿 `migrations_runner.go` 的静态扫风格） | expand-contract 门禁 |
| `backend/migrations/README.md` | 加「蓝绿 expand-contract 规则」：破坏性变更拆两次发布（先 expand 双写、下版 contract 删旧），即「N 版代码必须能在 N+1 schema 上正常跑」 | 文档约束 |
| `.github/workflows/deploy-stage0.yml` | 新增「sync compose+Caddyfile」步骤（幂等保险）；「Deploy via SSM」改调 `deploy_via_ssm_bluegreen.sh`；可选加 `operation=rollback`（秒级换回旧色） | prod workflow |
| `.github/workflows/deploy-edge-*.yml` | **不改** | edge opt-out |

### DB 迁移 expand-contract（最大语义风险）

蓝绿重叠窗口内新旧 schema 代码同连一个库。Advisory Lock（ID `694208311321144027`）已串行化并发迁移、checksum 跳过已应用——**并发安全已具备**。危险在**语义**：若新版带破坏性迁移（DROP/RENAME/NOT NULL），旧色（N 版代码）在「新色 healthy 但还没 drain 旧色」窗口里业务请求会 500，破坏「零影响」承诺。现有迁移以 `ADD COLUMN`/`CREATE TABLE`/`CREATE INDEX CONCURRENTLY`/seed 为主（expand 友好），但存在 `DROP` 类。门禁 `check_migration_bluegreen_safe.sh` 把「记得 expand-contract」变成机械检查，不可省。

---

## 4. 现存 prod 实例 cutover（单色 → 蓝绿，一次性、尽量零停机）

现存 prod 跑 `container_name: tokenkey`。cutover 顺序敏感（封装成 `cutover_to_bluegreen_via_ssm.sh`，先在本地演练）：

1. 完成 §3 改动，`build-cfn.sh` 刷新两 CFN 的 compose/caddy blob，`build-cfn.sh --check` CI 绿，发 release tag。
2. `sync_compose_via_ssm.sh prod <iid>` 把蓝绿 compose 落盘（**不 up**，老 `tokenkey` 仍服务、零影响）。
3. `docker compose up -d tokenkey-blue`（用当前 prod tag 起 blue，与老 `tokenkey` 并存、连同一 DB/Redis、都健康）→ 等 blue healthy。
4. `sync_caddyfile_via_ssm.sh prod <iid>` 落双 upstream Caddyfile + `caddy reload`（Caddy 现在能路由到 blue）。
5. `docker kill -s USR1 tokenkey` drain 老容器、等 `in_flight=0` → `docker compose rm -sf tokenkey`（`--remove-orphans` 清孤儿）→ 写 `active-color=blue`。
6. CFN stack update 把蓝绿 blob + SwapSizeGiB 持久化到 Parameter Store（reboot 持久性；数据卷 Retain 保数据）。
7. 跑一次 `deploy_via_ssm_bluegreen.sh` 空发版（同 tag 切到另一色）验证链路。

---

## 5. 端到端验证（本地 stage0 docker）

复用 `tokenkey-stage0-local-deploy` skill 的本地栈（:8088）：

1. 蓝绿 compose 起 `postgres redis caddy tokenkey-blue`（`TOKENKEY_IMAGE_BLUE=<tagA>`），`curl :8088/health=200`。
2. **零停机探针**：后台 `TOKENKEY_BASE_URL=http://localhost:8088 INTERVAL_SECONDS=0.05 bash ops/stage0/measure_deploy_blackout.sh`（或直接 Monitor）。
3. 触发切换：`TOKENKEY_IMAGE_GREEN=<tagB>` → `up -d tokenkey-green` → 等 healthy → sleep 12（Caddy 探到 green）→ `docker kill -s USR1 tokenkey-blue` → 轮询 `health/inflight` 到 0 → `stop tokenkey-blue`。
4. **判定零停机**：探针全程无 502/503/000（drain 旧色的 503 只被 Caddy 内部消费）。可并发一个长 SSE 验证切换中不中断。
5. **失败零影响**：把 `TOKENKEY_IMAGE_GREEN` 设成坏镜像 → green 永不 healthy、脚本在 drain 前 `exit 1`、blue 从未被动、探针全程 200。
6. **Caddy 容忍缺失色**：只起 blue，确认配置加载成功、green 是 active-health-unhealthy 而非 fatal。

---

## 6. 风险与回滚

| 风险 | 缓解 |
|---|---|
| **prod/edge 共享 compose blob**（最高优先级耦合） | 必须先拆 `docker-compose.edge.yml`，否则 edge 被迫双色、1GB micro 内存爆。落地前置依赖，不可跳过。 |
| **重叠窗口内存峰值**（t4g.small 2GB） | 加 swap + `mem_limit`/`GOMEMLIMIT`；验证时看 `docker stats`/`free -m`；fallback 升 t4g.medium |
| **破坏性迁移打挂旧色** | `check_migration_bluegreen_safe.sh` 门禁 + README 约束；不可省 |
| **Caddy 探测时延**（`sleep 12` 经验值，≥2×health_interval） | 与 health_interval 同步调；§5 步骤 4 覆盖此风险 |
| **cutover 一次性窗口顺序敏感** | 封装专用脚本，先本地演练 §5 再上 prod |

**回滚（分层）**：① 蓝绿发版失败 → 脚本 drain 前 `exit 1`，旧色零影响；② 上线后发现问题（已切色）→ `operation=rollback` 重新拉起上一色（仍持上版镜像）、drain 当前色、翻 `active-color`，秒级；③ 蓝绿方案本身出问题 → `sync_compose_via_ssm.sh` 同步回单色版（含 `.before-<ts>` 备份 + ERR trap），回到 `deploy_via_ssm.sh` 单色发版，整能力一键回退到现状。

---

## 参考

- Plan 设计来源：本仓库 plan `typed-fluttering-waterfall.md`（会话产出）
- 现状发版原语：`ops/stage0/deploy_via_ssm.sh`（头部注释详述 drain/swap 时序与 uk1 2026-06-03 教训）
- Caddy 热同步范式：`ops/stage0/sync_caddyfile_via_ssm.sh`
- Caddy docs：reverse_proxy directive / dynamic upstreams / issue #3459
