# 同机蓝绿零停机发版 — Prod 已启用 / 后续增强 Backlog

> **状态：第一阶段已实施。** prod workflow 现在走 `ops/stage0/deploy_via_ssm_bluegreen.sh`：
> 同机 `tokenkey-blue` / `tokenkey-green` 双 app，Postgres/Redis/Caddy/数据卷仍是单数据层。
> 该脚本会自迁移现存单色 `tokenkey` 容器，并持久化 `/var/lib/tokenkey/active-color`、
> `/var/lib/tokenkey/docker-compose.bluegreen.yml` 和 blue/green 版 `tokenkey.service`。
> 本文下方保留更完整的“拆 prod/edge compose、专用 rollback operation、本地 e2e”等后续增强设计。
> 配套：常规发版入口见 `deploy/aws/README.md` §升级；灾难恢复见 `deploy/aws/RUNBOOK-disaster-recovery.md`；
> 真空实测工具 `ops/stage0/measure_deploy_blackout.sh`。

## 为什么解封

2026-06-24 prod 发版实测出现用户可感知 5xx：单 app 发版期间 Caddy 5xx 窗口约 30s，窗口内 176 个 503 + 2 个 502，主因是单容器 drain 后没有备用健康 upstream。prod 当前 `t4g.large` 资源足够承载短暂双 app 重叠，因此触发本方案第一阶段落地。

第一阶段刻意不改共享 `deploy/aws/stage0/docker-compose.yml`，因为该文件仍被 Lightsail edge bootstrap 复用。prod blue/green 布局由 SSM 脚本在 live host 上幂等生成；edge 继续使用 `deploy_via_ssm.sh` 单 app 路径。

## 1. 后续增强触发项

- **① compose 源头解耦**：把 prod/edge compose 分开，让蓝绿布局进入 CFN/SSM 参数源，而不是只靠 live-host 生成。
- **② 秒级 rollback operation**：给 `deploy-stage0.yml` 增加 rollback 输入，直接切回保留的上一色，而不是通过“重新部署旧 tag”完成。
- **③ 本地 e2e 固化**：在 local stage0 栈里跑 measure_deploy_blackout，对切色过程做自动化零 5xx 断言。
- **④ 多实例 / 外部 LB**：当单机可用性目标不够时，升级到跨实例 blue/green 或 ALB/Cloud Map；本文当前只覆盖同机双 app。

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

**历史结论已过期**：2026-06-24 prod 数据证明单 app 空窗会产生用户可感知 5xx，因此已转为第一阶段实施。

---

## 2. 核心机制（第一阶段已落地）

第一阶段采用 **Caddy 单 active upstream + reload 切色**，不是“双 upstream 自动分流”。原因：如果 Caddy 同时配置 blue/green，target 一旦 `/health=200` 就可能在正式 cutover 前接新流量；单 active upstream 可以把“开始接新请求”绑定到显式 Caddy reload，时序更可控。发版时短暂跑两个 app，平时只保留当前 active 色：

1. 读 `active-color`（`/var/lib/tokenkey/active-color`，blue/green），target = 另一色；
2. 给 target 设新镜像 tag，`compose up -d <target>`，等 Docker health `/health/live` 和 readiness `/health` 都通过；
3. `caddy validate` 新配置后 reload 到 target upstream，新请求开始进入 target；
4. SIGUSR1 drain 旧色，等 `in_flight=0` 或 plateau；
5. 写 `active-color=target` 并持久化 blue/green 版 `tokenkey.service`；
6. 停旧色容器。
   **失败时**（target 没 healthy）：旧色从未被 drain/切走，零影响，停掉失败 target 即可。

### 已验证的技术结论

- **Caddy 切色原子性**：第一阶段只改 live Caddyfile 的唯一 `reverse_proxy` upstream，保留其余 canonical / hot-sync 指令；再用 `caddy validate` + `cat > Caddyfile` 保 inode + `caddy reload`。reload 失败会写回旧 Caddyfile 并 reload 回旧配置；Caddy reload 成功后脚本不再自动删除 target，避免误删已开始服务的新 upstream。
- **drain 是进程级、无同名容器假设**：`drainFlag` 是每进程独立的 `atomic.Bool`；`docker kill -s USR1 tokenkey-blue` 只 drain blue 进程。`/health`（drain-aware→503）/ `/health/live`（恒 200，docker healthcheck 用）/ `/health/inflight`（loopback-only，脚本须 `docker exec <color> wget localhost:8080/...`）。无 `SetDrain(false)` 调用点 → 新色必须是全新 `up`（drain=false 初始），换色天然清 drain，**不需要单色模式那条 `--force-recreate` load-bearing 逻辑**。
- **内存**：当前 prod `t4g.large` 足够短暂双 app 重叠。后续若把蓝绿布局固化进 CFN，可再加 app `mem_limit` / `GOMEMLIMIT` 钉峰值。
- **edge 不上蓝绿**：edge 是 t4g.micro（1GB），双色即便有 swap 也重度换页；edge 是薄 relay 节点、发版影响面小，维持现状单色 drain-recreate。**方案按节点 opt-in：prod 蓝绿，edge 现状。**

---

## 3. 文件改动清单（实施时）

> 下表标「新增」的脚本均位于 `ops/stage0/`（实施时创建；此处用裸文件名，因为它们尚不存在，
> 写全路径会被 `scripts/checks/script-ref-existence.py` 误判为悬空引用）。

| 文件 | 改动 | 关键点 |
|---|---|---|
| `deploy/aws/stage0/docker-compose.yml` | 引入 `x-tokenkey-app` YAML anchor，`tokenkey` 拆成 `tokenkey-blue` + `tokenkey-green`（各自 `container_name` + `TOKENKEY_IMAGE_{BLUE,GREEN}`）；加 `mem_limit`/`GOMEMLIMIT`；caddy `depends_on` 收敛到只依赖 postgres/redis | **此文件被 prod+edge 共享 embed，必须先拆 edge** |
| `deploy/aws/stage0/docker-compose.edge.yml`（新增） | edge 保留单色 `container_name: tokenkey`（=现状），与 prod 解耦 | 解决共享耦合 |
| `deploy/aws/stage0/Caddyfile` | 第一阶段**不改** canonical 文件，仍保留 `reverse_proxy tokenkey:8080`；live host 由 `deploy_via_ssm_bluegreen.sh` / `sync_caddyfile_via_ssm.sh` 只重写 upstream host 为 active 色 | 单 active upstream，避免 cutover 前 target 提前接流量 |
| `deploy/aws/stage0/Caddyfile.edge` | **不改**（edge 单色 `tokenkey:8080`） | edge opt-out |
| `deploy/aws/stage0/build-cfn.sh` | 拆 `COMPOSE_SRC` 为 prod / edge 两个 blob，分别 embed 进 single-ec2 / edge-ec2（仿 Caddyfile 已有的 prod/edge 分流），新增 `EDGE_COMPOSE_GZB64_SSM` marker | prod/edge compose 解耦 |
| `deploy/aws/cloudformation/stage0-single-ec2.yaml` | 加 `SwapSizeGiB`（抄 edge swapfile UserData）；`COMPOSE_GZB64_SSM` 指向蓝绿 compose；UserData 首启写 `active-color=blue` 并只 `up` active 色 | prod 加 swap + 蓝绿首启 |
| `deploy/aws/cloudformation/stage0-edge-ec2.yaml` | `EDGE_COMPOSE_GZB64_SSM` 指向单色 compose（功能等价现状） | edge 保持单色 |
| `sync_compose_via_ssm.sh`（新增） | 热同步 compose 到 live host（类比 `ops/stage0/sync_caddyfile_via_ssm.sh`，但无 envsubst、可直接 `gunzip >` 覆盖，只落盘 + `compose config -q` 校验、不 `up`） | 现存实例落地 |
| `ops/stage0/deploy_via_ssm_bluegreen.sh` | **已实施**。蓝绿切换原语，含单色 `tokenkey` → `tokenkey-blue` 自迁移、Caddy 切色、systemd 持久化 | prod 发版 |
| `cutover_to_bluegreen_via_ssm.sh`（新增） | **不再需要第一阶段**：自迁移已合入 `deploy_via_ssm_bluegreen.sh` | 仅后续拆分时考虑 |
| `scripts/checks/bluegreen-migration-safety.py` | **已实施**。对本次新增/修改迁移静态扫 `DROP COLUMN`/`DROP TABLE`/`SET NOT NULL`/`ADD COLUMN ... NOT NULL`/`RENAME`/`ALTER...TYPE`，命中需迁移头注释显式标记才放行 | expand-contract 门禁 |
| `backend/migrations/README.md` | 加「蓝绿 expand-contract 规则」：破坏性变更拆两次发布（先 expand 双写、下版 contract 删旧），即「N 版代码必须能在 N+1 schema 上正常跑」 | 文档约束 |
| `.github/workflows/deploy-stage0.yml` | **已实施**：Deploy via SSM 改调 `deploy_via_ssm_bluegreen.sh`；后续可选加 `operation=rollback`（秒级换回旧色） | prod workflow |
| `.github/workflows/deploy-edge-*.yml` | **不改** | edge opt-out |

### DB 迁移 expand-contract（最大语义风险）

蓝绿重叠窗口内新旧 schema 代码同连一个库。Advisory Lock（ID `694208311321144027`）已串行化并发迁移、checksum 跳过已应用——**并发安全已具备**。危险在**语义**：若新版带破坏性迁移（DROP/RENAME/NOT NULL/ADD COLUMN NOT NULL），旧色（N 版代码）在「新色 healthy 但还没 drain 旧色」窗口里业务请求会 500，破坏「零影响」承诺。现有迁移以 `ADD COLUMN`/`CREATE TABLE`/`CREATE INDEX CONCURRENTLY`/seed 为主（expand 友好），但存在 `DROP` 类。门禁 `scripts/checks/bluegreen-migration-safety.py` 把「记得 expand-contract」变成机械检查，不可省。

---

## 4. 现存 prod 实例 cutover（第一阶段已内置）

现存 prod 跑 `container_name: tokenkey`。第一阶段没有单独脚本，`ops/stage0/deploy_via_ssm_bluegreen.sh` 首次运行会自动完成：

1. 读取 legacy `tokenkey` 的当前镜像，生成 `/var/lib/tokenkey/docker-compose.bluegreen.yml`；
2. 用当前镜像启动 `tokenkey-blue`，等待 `/health/live` 和 `/health`；
3. Caddy reload 到 `tokenkey-blue`；
4. SIGUSR1 drain legacy `tokenkey` 后停止并删除；
5. 写 `active-color=blue`，安装 blue/green 版 `tokenkey.service`；
6. 再把本次发版 tag 部署到另一色，完成第一次真实切色。

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
| **破坏性迁移打挂旧色** | `scripts/checks/bluegreen-migration-safety.py` 门禁 + README 约束；不可省 |
| **Caddy 探测时延**（`sleep 12` 经验值，≥2×health_interval） | 与 health_interval 同步调；§5 步骤 4 覆盖此风险 |
| **cutover 一次性窗口顺序敏感** | 封装专用脚本，先本地演练 §5 再上 prod |

**回滚（分层）**：① 蓝绿发版失败 → Caddy reload 前 `exit 1`，旧色零影响；② 上线后发现问题（已切色）→ 重新 dispatch 上一版 tag，脚本拉起 inactive 色、健康后切回并 drain 当前色；③ 蓝绿方案本身出问题 → 手工恢复 legacy `tokenkey` compose/service 后，workflow 可临时改回 `deploy_via_ssm.sh` 单色发版。

---

## 参考

- Plan 设计来源：本仓库 plan `typed-fluttering-waterfall.md`（会话产出）
- 现状发版原语：`ops/stage0/deploy_via_ssm.sh`（头部注释详述 drain/swap 时序与 uk1 2026-06-03 教训）
- Caddy 热同步范式：`ops/stage0/sync_caddyfile_via_ssm.sh`
- Caddy docs：reverse_proxy directive / dynamic upstreams / issue #3459
