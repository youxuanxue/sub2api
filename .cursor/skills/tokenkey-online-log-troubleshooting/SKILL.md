---
name: tokenkey-online-log-troubleshooting
description: >-
  Read-only TokenKey prod/edge troubleshooting workflow. Use for live logs, ops_error_logs, SSM/Docker checks, gateway UA/TLS/body evidence, CI/deploy traces, and evidence-based root-cause summaries.
---

# TokenKey：线上日志查询与问题定位

适用于本仓库（TokenKey fork of sub2api）的 prod / edge Stage0 线上排障。目标是把“识别环境 → 只读采样 → 聚合证据 → 定位根因 → 给出最小动作建议”固定成稳定流程，避免每次临时猜容器名、SQL 字段、SSM 参数或时间窗口。

权威纪律以仓库根 `CLAUDE.md` 为准；本 skill 默认**只读**。任何写线上配置、重启容器、部署、删数据、改分支或发 PR comment 都必须另行显式确认。

## 确定性基线（机械化 vs 真判断）

机械采集用 `run-probe.sh`：默认先 `ops-error-triage.sh` 聚合、`probe-tail-gateway-logs.sh` 取脱敏样本；429 细分用 `probe-429-classify.sh`，镜像 edge 健康用 `scan-edge-health.sh`。根因/风险由证据判断。按症状找其它 probe（计费、UA/TLS、dashboard、CI）时读取目录；标为写操作的 remediation 不属于本 skill 的默认只读范围。 见 [操作细则](references/probes.md)。

## 调用参数

```text
/tokenkey-online-log-troubleshooting target=<prod|edge:<id>|domain> issue=<描述> [time_window=<ISO区间|last_Nh|since>] [scope=<gateway|ops|deploy|ci|db|all>] [request_id=<id>] [user_id=<id>] [api_key_id=<id>] [model=<name>] [path=<path>] [mode=<triage|deep|watch>] [allow_planned=true|false]
```

| 参数 | 语义 |
|---|---|
| `target` | `prod`、`edge:us1` / `edge:uk1` / `edge:fra1`，或用户给出的域名。 |
| `issue` | 用户描述的症状、错误 JSON、request_id、时间点或“昨晚/刚才”等自然语言。 |
| `time_window` | 优先使用明确 ISO 区间；缺省时根据 issue 推断，并输出 UTC 与本地时间。 |
| `scope` | 默认 `all`；可收窄到 `gateway`、`gateway_fingerprint`、`gateway_debug`、`ops`、`deploy`、`ci`、`db`。 |
| `request_id` | 上游 request id 或 TokenKey request id；用于精确查日志。 |
| `user_id` / `api_key_id` | 用户侧定位字段；没有就不要猜。 |
| `model` / `path` | 过滤 `/v1/messages`、模型、OpenAI/Gemini/NewAPI 路径等。 |
| `mode=triage` | 默认：小输出、聚合优先，目标是 1 次定位方向。 |
| `mode=deep` | 聚合后再查样本详情；仍避免输出大 request body。 |
| `mode=watch` | 用户明确要求持续盯时才用 Monitor / gh watch。 |

默认行为：
- 用户说“查线上日志 / 看 edge / 定位错误” → `mode=triage`、`scope=all`、只读。
- 用户给了错误 JSON 但没给时间 → 从当前时间向前 24h，且说明假设。
- 用户给“昨晚/刚才” → 转成明确 UTC 区间，并在输出里同时给本地时间。
- 用户要求“修复/调整配置” → 先完成 triage 和 plan，不直接 apply。

## 0) 稳定性原则（真判断）
1. **先识别环境，不猜容器名。** 先解析 target，再远端 `docker ps` / `docker compose ps`；不要硬编码 `tokenkey-app`、`postgres` 等旧名。当前常见容器名是 `tokenkey`、`tokenkey-postgres`、`tokenkey-redis`、`tokenkey-caddy`，但仍以 live 输出为准。
2. **先查 schema，不猜列名。** 查询新表或不确定字段前，用 `information_schema.columns` 或 `SELECT row_to_json(t) LIMIT 1` 确认可用列；避免猜 `enabled`、`tpm_limit`、`rpm_limit` 等。
3. **先 count/aggregate，再 sample。** `ops_error_logs` 先做 count/by_status/by_kind/by_minute；只有需要时再取少量样本。
4. **小输出优先。** SSM stdout 易截断；默认输出聚合，不 dump 大 body / 全日志。大结果写 `$CLAUDE_JOB_DIR` 本地或远端安全临时文件，只回传摘要和路径。
5. **UTC + 本地时间双写。** DB 与 Docker logs 统一用 UTC ISO；用户报告使用 Asia/Shanghai 时同时标注换算。
6. **区分最终失败和中间 upstream event。** `ops_error_logs.status_code` 是最终用户侧状态；`upstream_errors[*]` 可能是被重试/降级恢复的中间错误。
7. **禁止泄漏敏感信息。** 不输出 Authorization、API key、cookie、完整 request_body、OAuth token、数据库密码。必要时只输出 sanitized/truncated 摘要。
8. **一次失败先修命令，不扩大权限。** SSM/SQL 失败时先读 error、修 quoting/schema；不要改线上状态来绕过。
9. **prod 的 `upstream-429 by account` / `recovered-200` 不反映镜像 edge 死活。**
   客户端断流与 failover 会把中间 429 归到链尾账号，因此不得把 prod 聚合值当作
   edge 健康主信号。跑 `ops/observability/scan-edge-health.sh`，以 edge 自身 access log 的
   `served_200 : no_available_429` 与可调度账号证据判定。

## 命令细节

Target 解析、SSM、`ops_error_logs` triage、access-log、UA/TLS、Admin perf、capacity、CI
排障的**完整命令**只在按症状从 [probe 目录](references/probes.md) 选择的脚本；本 skill 不复述。统一入口：

```bash
bash ops/observability/run-probe.sh --target prod|edge:<id> --script <probe.sh>
```

## 8) 决策输出模板

```text
target=<prod|edge:id|domain>
time_window_utc=<start>..<end>
time_window_local=<start>..<end>
mode=<triage|deep|watch>

symptom=<用户报告>
evidence:
- <聚合数字，区分 final status 与 upstream events>
- <相关账号/分组/版本配置>
- <日志样本 request_id / kind / status，已脱敏>

root_cause=<最可能根因，置信度 high|medium|low>
not_root_cause:
- <被排除项及证据>

recommended_action:
1. <最小动作，是否需要确认>
2. <验证方式>

validation_query:
- <调整后应看的指标 / SQL / gh checks>
```

如果证据不足：

```text
needs input:
需要更明确的 time_window 或 request_id；当前 24h 聚合无法区分多个用户/事件。
```

## 9) 常见失败与固定处理

仅出现 SSM、SQL、凭证或日志查询失败时读取对应处理。 见 [操作细则](references/troubleshooting.md)。

## 10) 交接给修复流程

本 skill 只负责稳定定位。需要改配置/代码时：
- 线上配置：先输出 plan + 固定确认口令；优先调用对应专用 skill（如 Anthropic OAuth 配置）。
- 代码修复：进入正常开发流程，先建任务/必要时 plan mode，改完跑 `scripts/preflight.sh`。
- 部署/rollout：调用 Stage0 release/deploy 专用 skill，不在本 skill 内临时执行。
- 应急止血（可调度池被陈旧冷却字段卡死、但已坐实**非真上游限流**）：`ops/observability/remediate-schedulable-pool.sh`（经 run-probe 投递，`MODE=edge-oauth-pool` 恢复 OAuth 池+补 `group_id` / `MODE=prod-mirror-cooldown` 清 cc-·kiro- 镜像冷却，before/after 自证）。它只清 cooldown 字段、reconciler 仍按真实信号重置，故是**缩短恢复窗口**而非掩盖真因——必须先有「非上游」结论再用。
