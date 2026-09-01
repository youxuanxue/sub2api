---
name: tokenkey-online-log-troubleshooting
description: >-
  Read-only TokenKey prod/edge troubleshooting workflow. Use for live logs, ops_error_logs, SSM/Docker checks, gateway UA/TLS/body evidence, CI/deploy traces, and evidence-based root-cause summaries.
---

# TokenKey：线上日志查询与问题定位

适用于本仓库（TokenKey fork of sub2api）的 prod / edge Stage0 线上排障。目标是把“识别环境 → 只读采样 → 聚合证据 → 定位根因 → 给出最小动作建议”固定成稳定流程，避免每次临时猜容器名、SQL 字段、SSM 参数或时间窗口。

权威纪律以仓库根 `CLAUDE.md` 为准；本 skill 默认**只读**。任何写线上配置、重启容器、部署、删数据、改分支或发 PR comment 都必须另行显式确认。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。这张表是未来 PR 编辑本 skill 时 reviewer 的核对抓手：新增「步骤」必须先按此分类。

| 步骤 | 类型 | 承载 |
|---|---|---|
| 解析 prod / edge target（region / instance_id / domain） | 机械 | edge 经 `ops/stage0/edge_ssm_execution.py`（Lightsail tag-SSM `EdgeId`/`Platform=lightsail` → `mi-*`）/ `resolve-edge-lightsail-target.py`；prod 经 CFN `describe-stacks` |
| SSM base64 投递 + send-command + poll | 机械 | `ops/observability/run-probe.sh --target prod\|edge:<id> --script <probe.sh>` |
| `ops_error_logs` 标准聚合（schema + by_status + upstream_events，保留 reason/截断 message + 429-by-minute） | 机械 | `ops/observability/ops-error-triage.sh`（通过 run-probe.sh 投递） |
| final-429 / 5xx 分类（config-cap vs 空池 #575 vs 真上游：by error_type/owner/phase + by group·model·account + 5min 桶） | 机械 | `ops/observability/probe-429-classify.sh`（通过 run-probe.sh 投递；`WINDOW_HOURS` 默认 3；triage 后的分类深挖） |
| SLA Dashboard 等价拆解（success/error_total/error_sla/client_faults + by_status owner 口径 + top SLA messages） | 机械 | `ops/observability/probe-sla-breakdown.sh`（通过 run-probe.sh 投递；`WINDOW_HOURS` 默认 24；对齐 Admin Ops `error_owner` SLA 公式） |
| 每日错误账本（SLA totals + final/recovered 分离 + new/regressed/persistent + access-log capture gap + repair eligibility） | 机械 | `ops/observability/probe-daily-error-ledger.sh`（只读采集）→ `daily_error_report.py build/aggregate/select`（脱敏、分类、稳定签名）；由 `ops-daily-diagnostics.yml` 调度，代码修复只交给无 AWS 权限的 `ops-repair-draft.yml` |
| ⚠写侧止血：恢复 anthropic 可调度 / 清陈旧冷却（`MODE=edge-oauth-pool` 恢复 OAuth 池+补 group_id / `prod-mirror-cooldown` 清 cc-·kiro- 镜像冷却；before/after 自证） | 机械(写) | `ops/observability/remediate-schedulable-pool.sh`（经 run-probe 投递；§10 交接修复时用，非只读、须先有结论） |
| Docker access log 解析（status/model/minute/latency 直方图 + marker 计数） | 机械 | `ops/observability/parse-access-log.py --stdin\|--file\|--docker` |
| live-host 运行态漂移（运行镜像 tag vs 部署 tag + deploy_via_ssm 注入的 env：SERVER_FRONTEND_URL / QA_BUNDLE_*）| 机械 | `ops/stage0/assert-live-host-state.sh <instance_id> [expected_tag]`（只读 SSM，advisory；verdict 逻辑+`--selftest` 在 `ops/stage0/live_host_state_verdict.py`，已进 preflight；deploy-stage0 部署后 + ops-daily-diagnostics 每日审计自动跑）|
| Gateway "http request completed" 最近 N 行 tail（脱敏 → JSON array，轻量原始日志） | 机械 | `ops/observability/probe-tail-gateway-logs.sh`（经 run-probe 投递；`LIMIT` 默认 50、`SINCE` 默认 24h、可选 `UNTIL` / `REQUEST_IDS` / `PATH_FILTER` / `STATUS_CODE` / `CONNECT_HOSTS`、`CONTAINER` 默认 auto；钉 `REQUEST_IDS` 时附带库表对账；`CONNECT_HOSTS` 只允许 `api-<id>.tokenkey.dev`，从 host+容器 GET `/health` 与 `/v1/messages`，host 再对 `/v1/messages` 发空 JSON POST（HTTP/1.1 与 HTTP/2）） |
| Dashboard 预聚合覆盖度诊断（"使用趋势只显示 2 天"：usage_dashboard_daily/hourly vs raw usage_logs + aggregation watermark） | 机械 | `ops/observability/probe-dashboard-aggregate-coverage.sh`（经 run-probe 投递；只读 `row_to_json`） |
| Dashboard 网关中转延迟 vs 端到端 duration（`gateway_latency_ms` 是否被 body 泵送污染；对比 usage_logs 与 usage_dashboard_daily 聚合） | 机械 | `ops/observability/probe-gateway-latency-dashboard.sh`（经 run-probe 投递；只读 `row_to_json`） |
| Admin UI access-log 性能画像（/admin 前端资源 + /api/v1/admin/* latency p50/p90/p95 + slow samples） | 机械 | `ops/observability/probe-admin-ui-perf.sh`（经 run-probe 投递；只读 Docker logs 聚合） |
| Admin UI API timing（逐页接口 curl TTFB/total/size/非 2xx，含 dashboard/usage/accounts/ops/payment 等页面形状） | 机械 | `ops/observability/probe-admin-ui-api-timing.sh`（经 run-probe 投递；只读 admin API key + curl，无 mutating endpoints） |
| Admin aggregation runtime config（dashboard aggregation env/config + hourly/daily/model/group marker 覆盖度） | 机械 | `ops/observability/probe-admin-aggregation-config.sh`（经 run-probe 投递；只读 env + SELECT） |
| Admin model rollup timing（dashboard/models 冷态慢：raw 7d group-by vs usage_dashboard_model_daily + raw today 耗时/一致性） | 机械 | `ops/observability/probe-admin-model-rollup-timing.sh`（经 run-probe 投递；只读 SELECT + EXPLAIN ANALYZE） |
| Admin group rollup timing（usage/dashboard group distribution 冷态慢：raw 7d group-by vs usage_dashboard_group_daily + raw today 耗时/一致性） | 机械 | `ops/observability/probe-admin-group-rollup-timing.sh`（经 run-probe 投递；只读 SELECT + EXPLAIN ANALYZE） |
| 图片/视频盯盘（成功计量计费 + 错误分面 + 计费异常 + last-seen；区分 image vs video、空池 429 vs 真上游错误 vs 缺权限 401） | 机械 | `ops/observability/probe-image-video-billing.sh`（窗口盯盘，`WINDOW_MIN`/`CTX_HOURS`）+ `ops/observability/probe-image-video-deepctx.sh`（openai 账号池/报错归属/流量出处一次性深挖），均经 run-probe 投递；只读 `row_to_json` |
| Studio 图片请求审计（Image Studio / BakeOff prompt 是否实际提交、是否同一轮、size/model 是否被前后端改写） | 机械 | `ops/observability/probe-studio-image-request-audit.sh`（经 run-probe 投递；查 `ops_system_logs component=audit.openai_image_request`；按 `WINDOW_MINUTES` / user / api_key / model / `studio_run_id` / `prompt_sha256` / request id 过滤） |
| 用户级盯盘（一组 user_id 的请求 + 错误 + 计量计费 + 图片/视频 breakout + last-seen，单次 SSM 往返，对齐 30min 汇报节奏） | 机械 | `ops/observability/probe-user-billing-watch.sh`（经 run-probe 投递；默认发现当前 `users.status=active AND deleted_at IS NULL` 清单，`USER_IDS` 可选覆盖、`WINDOW_MINUTES` 默认 30；只读 `row_to_json`，复用 probe-image-video-billing.sh 的 image/video 判别谓词） |
| Kiro 响应兼容历史（按日核对指定/全部模型的 Kiro 承接账号、stream 比例、匿名 user id、User-Agent 版本与匹配错误事件） | 机械 | `ops/observability/probe-kiro-response-compat.sh`（经 run-probe 投递；`DAYS` 默认 15、`MODEL` 默认 `claude-opus-4-8` 且 `*` 表示全部、`UA_LIMIT` 默认每日 20、`UA_FILTER` 可选；只读 `usage_logs`/`ops_error_logs`/`accounts`，不读请求或响应 body） |
| Gateway UA/TLS / usage_logs / ops / docker 指纹交叉对比（窄时间窗） | 机械 | `ops/observability/probe-gateway-ua-tls-compare.sh`（通过 run-probe.sh 投递；`WINDOW_MINUTES` 收窄 DB 窗） |
| OpenAI/Python ingress → edge OAuth mimic 出站（HTTP 头 + system，非 UA-only） | 机械 | `ops/observability/probe-oauth-mimicry-chain.sh`（edge + `PLATFORM=anthropic`）；日志 `gateway.anthropic_oauth_mimic_egress` |
| `ops_error_logs.request_body` 顶层参数形状聚合（若线上 schema 保留 body，则只输出 top-level keys / deprecated sampling key 存在性；若无 body 列则输出 schema-unavailable + 错误样本） | 机械 | `ops/observability/probe-ops-error-request-shape.sh`（经 run-probe 投递；用于确认错误请求是否携带 `temperature` / `top_p` / `top_k` 等字段，不输出 prompt/body 原文） |
| final error 与 QA evidence 覆盖率（request_id 关联、retention、blob ref/本地存在性；不输出正文或 URI） | 机械 | `ops/observability/probe-qa-error-evidence.sh`（经 run-probe 投递；先判断是否有可深挖证据，再决定是否需要隐私受控的正文检查） |
| `SUB2API_DEBUG_GATEWAY_BODY` 日志拉回本机（SSM gzip → S3 presigned PUT → 本地 gunzip） | 机械 | `ops/observability/fetch-gateway-debug-log.sh --target prod\|edge:<id>`（**本地** orchestrator，不走 run-probe） |
| anthropic capacity / cap 与 schedulable 证据 | 机械 | `ops/observability/probe-caps.sh`（已有，通过 run-probe.sh 投递）/ `ops/anthropic/manage-anthropic-config.py snapshot` |
| Scheduler snapshot 桶 vs DB 对齐（`ListSchedulableAccounts` mixed/single 路径：Redis `sched:*` zcard、DB 可调度池、outbox 尾；排查 total=0 时区分本地空池 vs edge relay 下游空池） | 机械 | `ops/observability/probe-scheduler-snapshot-bucket.sh`（经 run-probe 投递；`GROUP_ID` 必填、`PLATFORM` 默认 gemini、`MODE` 可选 mixed/single/forced） |
| 镜像 edge 死活/容量判定（fleet 横扫：served_200:no_available_429 + 可调度账号数 → verdict） | 机械 | `ops/observability/scan-edge-health.sh`（本地 fan-out 全 deployable edge）/ 单边远端 `probe-edge-health.sh` + 纯函数 `edge_health_verdict.py`（`--selftest` 已进 preflight） |
| 时间窗规范（UTC ↔ Asia/Shanghai 双写） | 判断 | prompt（含报告口径，无机械抓手） |
| 解读规则：final_status vs upstream events、镜像账号链式失败、prod upstream-429 不反映 edge 死活 | 判断 | prompt（架构判断，§0 列出 9 个 trap 已固化） |
| 根因 / 风险分级 / 建议下一步 | 判断 | prompt（爆炸半径 / 回滚成本） |

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
9. **prod 的 `upstream-429 by account` / `recovered-200` 不反映镜像 edge 死活——别拿它当 triage 主信号。** 这两个数被双重灌水：(a) 客户端断流（context canceled）在重试/failover 中途被打上残留 `upstream_status=429`；(b) `shouldFailoverUpstreamError` 对 429 也 failover，死 edge 的 429 沿 failover 链涂抹到链尾每个账号。2026-06-06 压测中 **edge-us5 实发 77 个 429，prod 却记 1266（16×）**；六个镜像 edge 的 prod upstream-429 都是 1272–1941，看着齐平，**底下从全健康（us5）到全宕 3.5h（us3：0×200）都有**。更反直觉：**`recovered-200` 越高 = edge 越死**（全靠 failover 救回，健康 edge 直接服务无需"恢复"）。判断 edge 死活的**唯一可靠口径** = edge **自身** access-log 的 `served_200 : no_available_429` 比 + 可调度账号数 → 跑 `ops/observability/scan-edge-health.sh`（见 `scan-edge-health.sh` / 基线表）。详见 memory「判断 edge 死活看 edge 自身比值非 prod upstream-429」与 §0 上方的 `upstream_error_rate` 假 P0 记忆。

## 命令细节

Target 解析、SSM、`ops_error_logs` triage、access-log、UA/TLS、Admin perf、capacity、CI
排障的**完整命令**只在上方确定性基线表里的脚本；本 skill 不复述。统一入口：

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

| 现象 | 常见原因 | 固定处理 |
|---|---|---|
| `No such container: tokenkey-app` | 容器名猜错 | 先 `docker ps`，以 live 名称为准。 |
| `column ... does not exist` | schema 演化或表名猜错 | 先查 `information_schema.columns`，改 SQL，不继续猜。 |
| SSM success 但 stdout 空 | JSON/heredoc quoting 未执行预期 | 改成 `psql -c` 或远端 Python wrapper；读 invocation JSON。 |
| SSM 输出截断 | dump 太大 | 聚合优先，limit/tail，远端写文件只回摘要。 |
| `WITH ORDINALITY` 报错 | JSONB lateral 写法错 | 用 `CROSS JOIN LATERAL jsonb_array_elements(...) AS e(value)`；需要 ordinality 时 `WITH ORDINALITY AS e(value, ordinality)`。 |
| 误把 recovered upstream error 当故障 | 只看 `upstream_errors` | 必须同时看 final `status_code`。 |
| 单窗口却 429 | CLI 内部多请求/多模型短峰 | 解析 access log by-minute，而不是按窗口数量判断。 |
| CI 查错 run | branch/run 未定位 | `gh pr checks` → `gh run view`，按 PR head SHA / branch 过滤。 |
| `probe-gateway-ua-tls-compare` DB 窗太宽 | 未设 `WINDOW_MINUTES`，outage 证据被 LIMIT 稀释 | 根据 issue 换算分钟数， `--env WINDOW_MINUTES=N`。 |
| `fetch-gateway-debug-log` SSM Failed | debug 文件不存在或 env 未开 | 远端 `docker exec tokenkey test -f /app/data/gateway_debug.log`；无文件则勿拉 body。 |
| S3 presign / curl PUT 失败 | 实例无外网或桶策略 | 读 SSM stderr；检查 `SSM_OUTPUT_S3_BUCKET` 与 IAM；勿改线上只为 bypass。 |
| 公网 `curl https://api-<edge>.tokenkey.dev` **连接超时** | Lightsail 防火墙缺 **TCP 443**（基线仅 443；SSM 内 curl 仍正常） | `aws lightsail get-instance-port-states`；`bash ops/stage0/verify-edge-lightsail-network.sh <id> --enforce-ports` |
| DNS 已指 Static IP 但 **TLS handshake 失败** / 证书错误 | provision 早于 DNS → ACME NXDOMAIN；Caddy 未续签 | DNS 生效后 `verify-edge-lightsail-network.sh <id> --renew-cert` |
| Edge SSM 找不到 instance | 仍按 EC2 CFN stack 查（edge 已全量迁 Lightsail，无 CFN） | `resolve-edge-deploy-route.py --json`；Lightsail 用 tag SSM `mi-*` |

## 10) 交接给修复流程

本 skill 只负责稳定定位。需要改配置/代码时：
- 线上配置：先输出 plan + 固定确认口令；优先调用对应专用 skill（如 Anthropic OAuth 配置）。
- 代码修复：进入正常开发流程，先建任务/必要时 plan mode，改完跑 `scripts/preflight.sh`。
- 部署/rollout：调用 Stage0 release/deploy 专用 skill，不在本 skill 内临时执行。
- 应急止血（可调度池被陈旧冷却字段卡死、但已坐实**非真上游限流**）：`ops/observability/remediate-schedulable-pool.sh`（经 run-probe 投递，`MODE=edge-oauth-pool` 恢复 OAuth 池+补 `group_id` / `MODE=prod-mirror-cooldown` 清 cc-·kiro- 镜像冷却，before/after 自证）。它只清 cooldown 字段、reconciler 仍按真实信号重置，故是**缩短恢复窗口**而非掩盖真因——必须先有「非上游」结论再用。
