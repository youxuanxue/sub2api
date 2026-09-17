## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。

| 步骤 | 类型 | 承载 |
|---|---|---|
| 解析 target（region / instance_id） | 机械 | `ops/stage0/edge_admin_resolve_target.py`（prod CFN；edge Lightsail SSM） |
| SSM base64 投递 + send + poll（probe-caps.sh / probe-traffic-logs.sh / profile-traffic.py 都通过它发） | 机械 | `ops/observability/run-probe.sh` |
| caps + 不可调度证据 + Redis 快照 + 近 2h 错误聚类 | 机械 | `ops/observability/probe-caps.sh`（输出每行一 JSON，`row_to_json`） |
| 拉 access log + sticky.scheduler_entry → /tmp/acc.txt / /tmp/sse.txt | 机械 | `ops/observability/probe-traffic-logs.sh` |
| 逐分钟重建 RPM / sticky / activeSess / conc | 机械 | `ops/observability/profile-traffic.py` |
| 全舰队短窗口请求/错误快照（usage_logs by 分钟/用户/模型/账号 + ops_error_logs by status + cc-* stub 传播） | 机械 | `ops/observability/probe-fleet-traffic-window.sh`（经 run-probe 投递；`WINDOW_MINUTES` 默认 5；逐分钟重建前的快速 snapshot） |
| 单用户短窗口请求/错误快照（+ gateway "http request completed" docker 日志 join 同一 user） | 机械 | `ops/observability/probe-user-traffic-window.sh`（经 run-probe 投递；`USER_ID` 必填、`WINDOW_MINUTES` 默认 5） |
| 历史 cost-window 累计（5h gauge 校准） | 机械 | psql `usage_logs` 派生（SKILL §3 末段 SQL） |
| 镜像 edge 死活/容量 fleet 横扫（served_200:no_available_429 + 可调度账号数 → verdict） | 机械 | `ops/observability/scan-edge-health.sh`（§4.1 双跳归因的确定性落点；verdict 纯函数 `edge_health_verdict.py` + `--selftest`） |
| §0 的 8 个 trap pattern（base_rpm 误判、列号陷阱、镜像账号链式失败、activeSess 上界） | 判断 | prompt（架构 + 历史现场判断） |
| §4 解读规则（哪个 cap 触顶） | 判断 | prompt（依赖同时段的 503 / 粘性 vs 非粘性现象） |
| 镜像账号双跳归因（prod cooldown → edge 实因） | 判断 | prompt（必须 edge 同时段画像，单跳不算结论） |
