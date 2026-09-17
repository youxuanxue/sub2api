---
name: tokenkey-online-log-troubleshooting
description: >-
  Read-only TokenKey prod/edge troubleshooting workflow. Use for live logs, ops_error_logs, SSM/Docker checks, gateway UA/TLS/body evidence, CI/deploy traces, and evidence-based root-cause summaries.
---

# TokenKey：线上日志定位

默认只读：识别环境 → 聚合 → 小样本 → 根因/最小动作。写配置、重启、部署、删数据或外部留言必须有对应授权，不从排障请求推导写权限。

## 目标与窗口

输入 `target=prod|edge:<id>|domain`、症状，按需附 time_window/request_id/user_id/api_key_id/model/path；未知值不猜。默认 `mode=triage scope=all`；deep 在聚合后查少量样本，watch 仅用户明确要求持续盯盘时使用。

明确 ISO 窗口优先；只有错误 JSON 无时间时先查过去 24h 并说明假设。「昨晚/刚才」转换为明确 UTC 区间，报告双写 UTC 与本地时间。

## 0) 稳定性原则（真判断）

- 先解析 target，再以 live `docker ps`/schema 确认容器和字段。未知列查 `information_schema.columns` 或 `row_to_json`，不靠列号。
- 先 count/by_status/by_kind/by_minute，再少量 sample。SSM 会截断长输出，大工件保存在安全临时目录，只报告摘要/路径。
- final `status_code` 与 `upstream_errors[*]` 分开；后者可能已被重试恢复。
- prod `upstream-429`/`recovered-200` 会受客户端断流和 failover 归因影响，不能判镜像 edge 死活；用 edge 自身 `served_200:no_available_429` 及可调度账号证据。
- 不输出 Authorization、keys/cookies/tokens、密码或完整请求 body。失败先修 quoting/schema/命令，不扩大权限或改线上状态绕过。

## 采集入口

机械解析和 SSM 投递由 `ops/observability/run-probe.sh` 负责；模型只解释证据。

```bash
bash ops/observability/run-probe.sh --target prod \
  --script ops/observability/ops-error-triage.sh
```

默认先聚合，再用 `probe-tail-gateway-logs.sh` 取脱敏样本；429 分类用 `probe-429-classify.sh`，镜像健康用 `scan-edge-health.sh`。计费、UA/TLS、dashboard、capacity、CI 等专门症状从 [probe 目录](references/probes.md) 选工具；参数看工具自身接口。目录中的 remediation 是写操作。

## 8) 决策输出模板

报告 target、UTC/本地窗口、症状、聚合/样本证据、最可能根因与置信度、排除项、最小动作及调整后验证。证据不足明确缺的窗口/request_id，不把聚合猜测当结论。

## 9) 常见失败与固定处理

遇到 SSM/SQL/凭证/查询失败读 [故障处理](references/troubleshooting.md)。

## 10) 交接给修复流程

配置先形成 plan，再走对应专用 skill 的写入门禁；代码走正常研发/preflight；部署走 Stage0 发布 skill。已坐实非真实上游限流且获得止血授权时，可用 `ops/observability/remediate-schedulable-pool.sh`（经 run-probe 投递）：`MODE=edge-oauth-pool` 恢复 OAuth 池并补 group，`MODE=prod-mirror-cooldown` 清镜像陈旧冷却；必须核对 before/after，不能掩盖上游限流。
