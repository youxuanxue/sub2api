# US Edge 生图容量与 8 GiB 过渡升级（2026-10-09）

## 范围与授权

本次会话明确批准：us3/us4/us5/us6 从 Lightsail small_3_0（2 vCPU / 2 GiB）升级至 large_3_0（2 vCPU / 8 GiB / 160 GiB，44 USD/月/台）。prod 规格和配置保持原样，仅临时暂停/恢复待迁移 Edge 的 prod 镜像调度。uk1/uk2 不在范围内。工作分支先快进对齐 origin/main `375fee82d2ee1262b2b18294fd660577f1744dcf`。

四台长期实例费用由 48 USD/月升至 176 USD/月，增加 128 USD/月。暂存旧实例即使停止仍收费，另有快照保留费用；本次不删除旧机或快照。

## 容量估算依据

历史观测窗口 UTC 01:20–04:00（CST 09:20–12:00）。按每条已完成请求的 `[completed_at - latency_ms, completed_at)` 重建在途区间，端点扫描求重叠并发；同时包含成功和失败。异步 202 提交及任务轮询不计入执行并发。OOM 中断且没有完成日志的请求不可重建，因此 us6 是可观测下界。prod 镜像在途和 Edge 实际执行并发不能互相替代。

| 主机 | 生图在途峰值 | 全部网关在途峰值 | 高峰资源证据 |
| --- | ---: | ---: | --- |
| prod | 111 | 192 | UTC 03:40:04，105 个生图在途，可用内存约 3005 MiB，CPU 8.6% |
| us4 | 25 | 27 | 已取得请求与主机采样对照 |
| us5 | 34 | 40 | UTC 02:10:20，23 个生图在途，可用内存 185 MiB，swap 969 MiB，CPU 95% |
| us6 | 37 | 45 | UTC 02:13:14，36 个生图在途，可用内存 32 MiB，swap 2048 MiB，I/O wait 48% |
| us3 | 未取得同期证据 | 未取得同期证据 | 当时 SSM 不在线，不用 prod 镜像数据冒充 Edge 执行并发 |

内存规划参考量为 `MemTotal - MemAvailable + SwapUsed - SwapCached`，包含后台和冷匿名页，不等于进程 RSS。容量关系用 `后台基线 + 峰值在途数 × 每请求内存包络 + 安全余量` 规划；由于没有历史进程 RSS/Go heap，本次不声称得到精确单请求内存系数，也不能由此证明内存泄漏。SAR CPU 是采样区间平均，内存是采样点，须按真实时间对齐。

us6 在 36 个生图在途的采样点，该规划参考量为 3.783 GiB；8 GiB 约为其 2.1 倍。此倍数只表示对已观测需求增加缓冲，OOM 截断意味着它不是需求上界。prod 在 105 个生图在途时该量约 4.706 GiB，仍有约 3 GiB 可用内存，支持本次暂不扩容 prod。

2 GiB 已出现几乎耗尽内存和 swap 的证据。8 GiB 是本次批准的内存过渡方案；CPU 核数不增加，仍需观察高峰 CPU 和延迟。历史总生图在途 111，在四台均分下约 28/台；单台维护时均分约 37/台，实际还受账号能力、粘性、非生图流量与分配不均影响，不能作为安全并发上限。原先面向高峰和 N-1 的规划是 4 vCPU / 16 GiB，但本次不执行该规格或 prod 扩容。

逐采样点数据见 [请求与主机对齐表](edge-image-capacity-20261009.csv)。

## 数据与用户体验保护

逐台迁移。记录全部平台镜像原 schedulable 状态，暂停目标节点新请求；SIGUSR1 排空，HTTP 在途和 Redis 异步 processing 任务连续三次归零才停写。停应用和定时任务后生成新 pg_dump，验证 S3 往返 SHA-256，并在隔离 PostgreSQL 中实际恢复和比对核心表数量。

普通 pg_dump 排除了 usage/ops/QA 日志数据，因此完整冷磁盘快照才是全量迁移载体。Redis 显式 SAVE，正常停止数据库和全部容器，关闭 Docker 自启动，生成 PostgreSQL、Redis、app、Caddy 和密钥配置文件的 SHA-256 清单，然后停止原机、创建完整快照。

新机从冷快照创建，业务保持停止，重新注册独立 SSM 身份。先比对全部冷文件、核验 8 GiB 内存，再保留原 Static IP 和防火墙策略启动原镜像，比对数据库核心表数量和镜像 ID。健康和真实业务验证通过后恢复原调度状态，再迁移下一台。

旧机保留停止状态。新机一旦启动应用，就可能产生计费、OAuth 刷新或其他写入，禁止直接回切旧磁盘；此时回退必须先排空新机并迁回最新状态。不能承诺绝对零延迟波动；单节点离线期间其他节点负载会增加。

## 执行状态

截至 UTC 08:59（CST 16:59），**四台尚未升配，仍为原 2 vCPU / 2 GiB；没有新实例创建、没有 IP 切换或原机删除**。

us6 已完成一次全量保护演练：HTTP/异步连续归零，新 S3 备份恢复核验成功，9,373 个冷文件 SHA-256 已记录，完整快照 `tokenkey-us6-cold-8g-20261009T081055Z` 已 available。核心表数量：accounts 40、api_keys 15、groups 17、settings 52、usage_billing_dedup 1,612,110、users 1；S3 往返与恢复比对一致。

首次新机注册创建被 operator 的 iam:PassRole 权限拒绝。因此在任何新机/IP 变更前，恢复 us6 原机、原镜像与原调度状态；恢复时再次比对核心表数量一致，公网 /health 200。us3/us4/us5 从未摘流或停机。

已补齐并实际通过 `prepare-resize` 的现有 OIDC 工作流，未修改 IAM 权限：

| Edge | Workflow run | 结果 |
| --- | --- | --- |
| us6 | [37907451348](https://github.com/youxuanxue/sub2api/actions/runs/37907451348) | success |
| us3 | [37907636895](https://github.com/youxuanxue/sub2api/actions/runs/37907636895) | success |
| us4 | [37907703041](https://github.com/youxuanxue/sub2api/actions/runs/37907703041) | success |
| us5 | [37907838833](https://github.com/youxuanxue/sub2api/actions/runs/37907838833) | success |

单次身份在生成后四小时过期；过期后必须重新准备，不能继续使用。us6 参数已本地解密验证目标，但从未打印 code。其余三台已提交在线预备快照以缩短后续冷快照等待，不作为最终一致性副本。

当前阻塞：operator 到 AWS SSM 管理 API 连续 TLS EOF，按会话 AGENTS.md 的连续三次失败纪律暂停切换，等待人工恢复管理网络。已停用的 us6 调度恢复并逐项验证。恢复执行必须重新检查剩余承接容量，再重新排空 us6、生成新备份和最终冷快照；之前快照已落后于恢复服务后的写入，禁止直接用于最终切换。

本地四项注册准备行为测试、完整 preflight 和实际四次准备 workflow 均通过。未宣称 8 GiB 新机、迁移后真实生图或高峰容量已经验收。运维工件暂存在 operator 的 `/tmp/tokenkey-edge8g-migration` 私有目录及 us6 `/var/lib/tokenkey/resize-20261009-8g`；不得把其中的账号或注册凭据原文提交。

