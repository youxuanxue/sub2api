---
name: tokenkey-online-traffic-profile
description: >-
  Read-only TokenKey prod/edge traffic profiling. Use to reconstruct per-minute RPM, sticky/load-balance split, sessions, concurrency, cap pressure, admin account-card gauges, or no-available-accounts throttling evidence.
---

# TokenKey：线上请求流量画像

只读重建 prod/edge 的逐分钟 RPM、sticky/session、并发与限额证据，用于趋势、admin gauge 核对及 `no available accounts` 归因。写配置、改 cap、重启或部署另交写入技能，须已有相应授权。

环境识别、SSM、UTC/本地时间及小输出纪律复用 [线上排查入口](../tokenkey-online-log-troubleshooting/SKILL.md)。采样与重建由 `ops/observability/run-probe.sh`、`probe-caps.sh`、`probe-traffic-logs.sh` 和 `profile-traffic.py` 承载；模型负责归因。其它 probe 见 [工具](references/tools.md)。

## 参数与窗口

```text
/tokenkey-online-traffic-profile target=<prod|edge:<id>|all-edges|domain> [hours=1] [minutes=<M>] [account=<id|name|all>] [model=<name>] [path=/v1/messages] [allow_planned=false]
```

- `minutes` 优先于 `hours`；拉日志时多取 2 分钟缓冲，再按 `completed_at` 过滤目标窗口。先确认容器启动时间与日志保留窗，缺失部分不能外推。
- `account=all` 先列目标 platform 的可调度账号。`all-edges` 由 `python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable` 解析；planned edge 仅在显式允许时查询。
- Lightsail 通过 Hybrid managed instance 的 `EdgeId` + `Platform=lightsail` 解析，不查旧 CFN InstanceId。
- 桶固定为分钟；更粗粒度在分钟结果上 rollup，不能用 `strftime` 表达 5 分钟桶。

## 执行与判断

1. 按 [采样细则](references/capture.md) 调用 `probe-caps.sh`，获取 cap、不可调度原因和当前 Redis 快照；字段使用命名 JSON，不临时拼多列 SELECT。
2. 调用 `probe-traffic-logs.sh` 拉 access/sticky 日志。传 `SINCE`、`PATH_KEY`、`CONTAINER`；检查脚本报告的 `acc_lines` / `sse_lines`，两者为零时排查格式、容器和保留窗。
3. 按 [重建参数](references/reconstruction.md) 调用 `profile-traffic.py`；仅涉及 5h cost-window 时展开对应 SQL。
4. 归因前读 [解释陷阱](references/interpretation-traps.md) 和 [归因规则](references/attribution.md)：历史来自日志，gauge 只代表当前；`base_rpm` 不是全部硬 cap。必须匹配同窗口 sticky、503/429 与 edge 自身证据，prod 中间 upstream 错误不等于 edge 故障。镜像账号必须给双跳证据。

## 输出与交接

报告用 key=value / JSON，每个数字带字段名，禁止无名列。包含 target、UTC/本地窗口、账号配置和可调度态、当前 cap 快照、历史峰值、触顶类型、置信度及支持证据；长分钟表存 `$CLAUDE_JOB_DIR`，只回摘要和路径。

原始值须能追到命名的 SSM stdout；重建值须能追到脚本输出及输入窗口。缺日志或无流量明确注明，可缩短窗口或换 target，不能编造完整画像。

需要调整 `base_rpm` / `max_sessions` / `concurrency` / priority 时输出计划，交 `tokenkey-anthropic-oauth-config`；`group.rpm_limit` 由 admin UI 独立设置。进一步故障定位交线上排查技能。
