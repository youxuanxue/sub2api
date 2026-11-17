---
title: cn-prod 多应用机滚动发版
status: approved
approved_by: "feng（2026-11-15 会话：批准。复审收敛后的版本）"
approved_at: 2026-11-15
risk: high
authors: [agent]
created_at: 2026-11-15
revised: 2026-11-15（按独立复审收敛：去掉 schema 升级、自动切通道、容量门槛与名单脚本；补启动即 drain、卡死恢复与锁）；2026-11-17（现网拓扑/容量与 `deploy/domestic` SSOT 对齐：app-b 4C8G、WRR 1:1、池 80、PG 400）；2026-11-17（drain 超时强排：503 且 in_flight≤10，见 decided.md）
---

# cn-prod 多应用机滚动发版

## 背景

2026-11-15 cn-prod 已切到「入口机 + 两台应用机 + 数据机」。**现网拓扑（2026-11-17）**以 `deploy/domestic/prod-target.json` + `deploy/domestic/README.md` + `compose.override.yml` 为准：

| 角色 | 实例 | 私网 | 现状（2026-11-17） |
|---|---|---|---|
| 入口机 | `lhins-j89o28j0` | `10.2.4.7` | Caddy 终止 `api.galaxywaveai.com` TLS，`weighted_round_robin 1 1`，主动健康检查 3s，连续 2 次失败摘除，`lb_try_duration 30s` |
| app-a | `lhins-6qmb3fd6` | `10.2.4.11` | 4C16G（`bundle_rs_mc_lg16_01`），单容器 `tokenkey`，监听 `10.2.4.11:8080` |
| app-b | `lhins-0kxx7cco` | `10.2.4.13` | 4C8G（`bundle_rs_mc_lg8_01`，由 2C4G 升配），单容器 `tokenkey`，监听 `10.2.4.13:8080` |
| 数据机 | `lhins-odyx5eis` | `10.2.4.5` | PG `max_connections=400`（2026-11-17 由 200 升，见审批）；源 ACL 已含 `.11` 与 `.13` |

> 历史快照（2026-11-15 设计批准时）：入口 WRR 曾为 `3 1`；app-a 仍蓝绿+socat；app-b 为 2C4G 手写 `compose.appb.yml`；PG `max_connections=200`。下文迁移步骤（M1–M6）描述的是当时要解决的问题，不是今日现状。

要解决的问题（2026-11-15 批准时）：

- 发版只认 `prod-target.json` 里的一台应用机。下次发版 app-b 会落后一个版本，同时按权重接 1/4 流量。
- 入口机和 app-b 没有看门狗。app-a 的看门狗测 `https://154.8.214.179/health`，这条是 app-a 直连，不经过入口机。
- app-b 开着 QA 抓取，但没有清理和归档 timer。

## 决定（用户 2026-11-15）

1. 不再做蓝绿，改为逐台滚动。
2. 远程执行以腾讯 TAT 为主、SSM 兜底。CI 使用 GitHub environment `tencent-ops`（仅 `main` 分支可用）里的 `TENCENTCLOUD_SECRET_ID/KEY`。
3. app-a 关闭公网 80/443，只经入口机对外。
4. 设计和实现合在一个 PR；prod 以后复用同一套实现，本 PR 只改 cn-prod。
5. 每一步都要用户无感：不中断请求，不出现 5xx。

## 组件

### 1. 身份：在 `prod-target.json` 上追加，不升 schema

`schema_version` 保持 1，追加 `entry` 和 `apps[]`。现有字段不动，约 30 个调用方不受影响：

```json
"public_origin": "https://api.galaxywaveai.com",
"tat_region": "ap-beijing",
"entry": {"lighthouse_instance_id": "lhins-j89o28j0", "private_ip": "10.2.4.7"},
"apps": [
  {"name": "app-b", "lighthouse_instance_id": "lhins-0kxx7cco", "private_ip": "10.2.4.13", "weight": 1},
  {"name": "app-a", "lighthouse_instance_id": "lhins-6qmb3fd6", "private_ip": "10.2.4.11", "weight": 1,
   "ssm_instance_id": "mi-06568963b4b3c3239"}
]
```

- `apps[]` 的顺序就是滚动顺序，金丝雀位在前（app-b）。权重现网为 1:1（2026-11-17；此前 app-a 权重 3）。
- `public_origin` 改成域名后，`host_https_watch.py` 就改测入口机，代码不用动。不过这一步要等 app-a 直连流量归零后再做（见 M3）。
- 加减应用机仍按 README 的清单逐项执行，不另写名单同步脚本。

### 2. 网关：启动即 drain，解除 drain（后端小改动）

- 一次性标记文件 `/app/data/start-draining`（宿主 `/var/lib/tokenkey/app/start-draining`，路径可用 `GATEWAY_START_DRAINING_MARKER` 覆盖）：进程启动时文件存在，`/health` 直接返回 503，入口机不会派流量。
- 新增信号 SIGUSR2：`SetDrain(false)` 并删除标记文件，`/health` 恢复 200。现有代码只有 `SetDrain(true)`（`backend/cmd/server/main.go:231`、`:244`）。
- 实现期修订（2026-11-15）：批准稿写的是环境变量 `GATEWAY_START_DRAINING=true`。环境变量放行后仍留在容器里，容器崩溃重启或宿主重启后会一直 503 并永久掉出轮转，所以改成放行即删除的标记文件。意图不变。
- 实现在 `internal/server/middleware/drain_control_tk.go` 与 `cmd/server/drain_signals_tk.go`，`main.go` 只加一行 `installTKDrainControls()`。
- 新版本必须先过本机验收才接流量；回滚和卡死恢复走同一条路径。

### 3. 远程执行：`deploy/domestic/remote_exec.py`

- 默认走 TAT（`tccli tat RunCommand` 加 `DescribeInvocationTasks`）。SSM 只能用 `--via ssm` 手动指定，不做自动切换。原因：只有 app-a 注册了 SSM；轮询超时时也无法证明命令没开始执行，自动切通道有可能让同一个变更跑两遍。
- 执行前做身份核对：`hostname -I` 必须包含该节点的 `private_ip`，否则拒绝。
- 只传小脚本，整文件校验一次 sha256。镜像继续走现有的 S3 预热分发。
- 输出只保留最后 8KB。脚本不 echo 环境变量，不 cat `.env`。

### 4. 滚动发版：`deploy/domestic/rolling.py`

单节点流程：

```
1. USR1 → /health 返回 503；等 10s（入口机 3s×2 摘除，再留余量）
2. 等本机 /health/inflight 中 in_flight=0（最长 10min）。超时后仅当 `/health` 仍为 503 **且** `0 < in_flight ≤ 10` 时**强排继续**（compose recreate 丢掉残留）；未进 503，或 `in_flight > 10`，abort 并 USR2 放回旧容器。
3. .env 写入新 TOKENKEY_IMAGE，宿主写标记文件 start-draining → docker compose up -d tokenkey
4. 容器 healthy（进程存活即可，/health 仍是 503）→ 在本机 127.0.0.1:18080 跑 candidate_smoke
5. USR2 → /health 返回 200 → 120s 内本机网关日志出现非健康检查请求，判定已回流
```

- **失败处理**：第 2 步超时：503 且 in_flight≤10 → 强排进第 3 步（告警 `forced drain`）；否则 abort。第 3、4 步失败时，先对新容器 drain，再用旧镜像以 drain 状态起容器，跑 smoke，然后 USR2。之后停止整个滚动，已完成的节点保持新版。第 5 步 120s 内没看到流量，只停止滚动、告警，不回滚，因为这台节点本身是健康的。
- **卡死恢复**：续跑时如果发现某个节点处于 draining（`/health` 返回 503），按它当前的 image id 处理：已是目标版本就从第 4 步继续；还是旧版本就从第 3 步继续。不会因为「有节点不健康」而拒绝续跑。
- **前置检查**：
  - 其余节点 `/health` 都是 200；
  - 每个节点的镜像都已预热，image id 与 receipt 一致；
  - `.env` 里的凭据与线上一致（沿用 #3209）；
  - 被摘节点之外的应用机 CPU 低于 70%，内存低于 `memory_limit` 的 60%。
- **最后一台**：最后一台 healthy 的节点不允许被摘流。
- **整次回滚**：用上一个 tag 再跑一遍。已经是目标 image id 的节点会跳过。

### 5. 锁

- CI 用 GitHub Actions 的 `concurrency: cn-prod-deploy` 保证同一时间只有一次发版。
- 本机手动执行时，在入口机写租约文件 `/var/lib/tokenkey/rolling-deploy.lease`，内容为持有者、开始时间、30min TTL，每完成一台续租一次。过期后可以带 `--take-over` 接管，接管时打印原持有者。

### 6. 看门狗与 QA

- **入口机**（新角色 `cn-prod-entry`）：
  - 监控公网 `/health`；
  - 证书剩余不足 20 天时告警；
  - 每台后端都直接探测私网 `/health`：持续返回 503 超过 15min 告警（发版 drain 最长 10min），所有后端同时不健康立即告警。
  - 不依赖 Caddy admin API 的健康字段，因为 `/reverse_proxy/upstreams` 只返回 `address`、`num_requests`、`fails`。
- **每台应用机**：本机 `/health`、QA timer、磁盘。
- **app-b 的 QA（2026-11-15 用户决定，延后）**：现有 cleanup 只认单源回执，app-b 不能照抄 app-a 的 timer。改为启用数据机协调器加 `force_retention.py`（`ops/qa/COORDINATOR.md`），本 PR 不做。app-b 每小时约 22MB、根盘余 41G，延后期间不会写满。

### 7. 节点布局

- 每台应用机只跑一个 `tokenkey` 容器，监听 `<private_ip>:8080` 和 `127.0.0.1:18080`。
- `compose.node.yml` 由 `prepare-runtime.py` 按 `apps[]` 渲染，替代主机上手写的 `compose.appb.yml`。
- 应用机上不再有 Caddy、socat 和 blue/green 容器。

## 迁移步骤（用户无感）

| # | 动作 | 回退 |
|---|---|---|
| M1 | 合并 PR，带新镜像正常发一次版。这一次仍走现有蓝绿，只发 app-a；app-b 手工换成同一镜像。新镜像要先上线，才有启动即 drain 和 USR2 | revert |
| M2 | app-b：渲染 `compose.node.yml`，与现有配置的 diff 必须为空；装 QA timer 和看门狗。入口机：Caddyfile 入库（diff 只差注释）并装看门狗 | 恢复原文件，删除 timer |
| M3 | 观测 app-a 公网直连请求，连续 6h 为 0 后把 `public_origin` 改成域名。11-15 实测直连仍有，正在逐步减少 | 改回 IP |
| M4 | app-a 改为单容器：green 走 drain（这段时间 app-b 单独承接，实测在途 3 个、CPU 8%）→ 停 socat → 以 drain 状态起 `tokenkey`，绑定 `10.2.4.11:8080` → smoke → USR2 → 停 green 和 Caddy → 云防火墙关闭 80/443 | 开启 80/443，起 Caddy、green、socat，入口机自动回流 |
| M5 | 用当前版本原样滚动一遍，作为演练 | 不改版本，失败就停 |
| M6 | ✅ 连续两次真实滚动成功后，删除 `bluegreen.py` 的 cn-prod 分支和 socat 相关代码（2026-11-17） | revert |

## 容量核算

**现网（2026-11-17）**：两台应用机各配 `DATABASE_MAX_OPEN_CONNS=80` / idle=20（`compose.override.yml`），合计最多 160 open；数据机 PG `max_connections=400`。`GATEWAY_MAX_IN_FLIGHT_REQUESTS=512`，`REDIS_POOL_SIZE=96`。prod 的池另算，在 prod 数据机上。依据：`docs/ops/reports/cn-prod-deepseek-flash-pool-perf-20261117.md`、`docs/ops/approvals/cn-prod-pg-max-connections-200-to-400.md`。

历史（2026-11-15 设计时）：各机 open=40 合计 80、PG 上限 200；app-b 仍是 2C4G/`memory_limit` 3g。M4 期间 app-b 单独承接全部流量须低峰执行——该迁移步骤已完成，数值勿当作今日 SSOT。

## 不变量

- 新旧版本共存不超过一次滚动发版。DB 迁移必须向前兼容，与蓝绿时的要求相同。
- 任意时刻至少一台应用机 `/health` 返回 200。
- 发版只改应用机，入口机和数据机的配置变更不走发版流程。
- 密钥只放在主机 `.env` 和 GitHub environment `tencent-ops` 里，不打印，也不进回执。

## 验收

- **后端单测**：标记文件存在时启动即 drain；USR2 后恢复 200 且删除标记，再次启动正常服务。
- **`rolling.py` 单测**：
  - drain 超时；
  - smoke 失败只回滚当前节点，且回滚也是先 drain；
  - 最后一台不能被摘流；
  - 节点卡在 draining 时（旧版和新版两种情况）续跑；
  - 租约过期接管。
- **`remote_exec` 单测**：身份核对不通过时拒绝执行；轮询超时不会自动重试。
- **线上验收（M5）**：
  - 滚动期间入口机日志 5xx 为 0，`reverse_proxy` 错误为 0；
  - 两台节点的 image id 一致；
  - 发版回执完整。
- **故障演练**：在 app-b 上故意让 smoke 失败，确认 app-b 回到旧镜像，app-a 不受影响，入口机日志 5xx 为 0。

## 不在范围内

- prod 迁移（单独方案，复用 `remote_exec` 和 `rolling`）。
- 第二台入口机。当前只依靠 DNS 回退。
- node1 归档故障（运维处理中）。
