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
| final-429 / 5xx 分类（config-cap vs 空池 #575 vs 真上游：by error_type/owner/phase + by group·model·account + 5min 桶） | 机械 | `ops/observability/probe-429-classify.sh`（通过 run-probe.sh 投递；`WINDOW_HOURS` 默认 3；§4 triage 的分类深挖） |
| SLA Dashboard 等价拆解（success/error_total/error_sla/client_faults + by_status owner 口径 + top SLA messages） | 机械 | `ops/observability/probe-sla-breakdown.sh`（通过 run-probe.sh 投递；`WINDOW_HOURS` 默认 24；对齐 Admin Ops `error_owner` SLA 公式） |
| 每日错误账本（SLA totals + final/recovered 分离 + new/regressed/persistent + access-log capture gap + repair eligibility） | 机械 | `ops/observability/probe-daily-error-ledger.sh`（只读采集）→ `daily_error_report.py build/aggregate/select`（脱敏、分类、稳定签名）；由 `ops-daily-diagnostics.yml` 调度，代码修复只交给无 AWS 权限的 `ops-repair-draft.yml` |
| ⚠写侧止血：恢复 anthropic 可调度 / 清陈旧冷却（`MODE=edge-oauth-pool` 恢复 OAuth 池+补 group_id / `prod-mirror-cooldown` 清 cc-·kiro- 镜像冷却；before/after 自证） | 机械(写) | `ops/observability/remediate-schedulable-pool.sh`（经 run-probe 投递；§10 交接修复时用，非只读、须先有结论） |
| Docker access log 解析（status/model/minute/latency 直方图 + marker 计数） | 机械 | `ops/observability/parse-access-log.py --stdin\|--file\|--docker` |
| live-host 运行态漂移（运行镜像 tag vs 部署 tag + deploy_via_ssm 注入的 env：SERVER_FRONTEND_URL / QA_CAPTURE_EXPORT_STORAGE_*）| 机械 | `ops/stage0/assert-live-host-state.sh <instance_id> [expected_tag]`（只读 SSM，advisory；verdict 逻辑+`--selftest` 在 `ops/stage0/live_host_state_verdict.py`，已进 preflight；deploy-stage0 部署后 + ops-daily-diagnostics 每日审计自动跑）|
| Gateway "http request completed" 最近 N 行 tail（脱敏 → JSON array，轻量原始日志） | 机械 | `ops/observability/probe-tail-gateway-logs.sh`（经 run-probe 投递；`LIMIT` 默认 50、`SINCE` 默认 24h、`CONTAINER` 默认 auto，按 active-color 解析 tokenkey-blue/green） |
| Dashboard 预聚合覆盖度诊断（"使用趋势只显示 2 天"：usage_dashboard_daily/hourly vs raw usage_logs + aggregation watermark） | 机械 | `ops/observability/probe-dashboard-aggregate-coverage.sh`（经 run-probe 投递；只读 `row_to_json`） |
| Admin UI access-log 性能画像（/admin 前端资源 + /api/v1/admin/* latency p50/p90/p95 + slow samples） | 机械 | `ops/observability/probe-admin-ui-perf.sh`（经 run-probe 投递；只读 Docker logs 聚合） |
| Admin UI API timing（逐页接口 curl TTFB/total/size/非 2xx，含 dashboard/usage/accounts/ops/payment 等页面形状） | 机械 | `ops/observability/probe-admin-ui-api-timing.sh`（经 run-probe 投递；只读 admin API key + curl，无 mutating endpoints） |
| Admin aggregation runtime config（dashboard aggregation env/config + hourly/daily/model/group marker 覆盖度） | 机械 | `ops/observability/probe-admin-aggregation-config.sh`（经 run-probe 投递；只读 env + SELECT） |
| Admin model rollup timing（dashboard/models 冷态慢：raw 7d group-by vs usage_dashboard_model_daily + raw today 耗时/一致性） | 机械 | `ops/observability/probe-admin-model-rollup-timing.sh`（经 run-probe 投递；只读 SELECT + EXPLAIN ANALYZE） |
| Admin group rollup timing（usage/dashboard group distribution 冷态慢：raw 7d group-by vs usage_dashboard_group_daily + raw today 耗时/一致性） | 机械 | `ops/observability/probe-admin-group-rollup-timing.sh`（经 run-probe 投递；只读 SELECT + EXPLAIN ANALYZE） |
| 图片/视频盯盘（成功计量计费 + 错误分面 + 计费异常 + last-seen；区分 image vs video、空池 429 vs 真上游错误 vs 缺权限 401） | 机械 | `ops/observability/probe-image-video-billing.sh`（窗口盯盘，`WINDOW_MIN`/`CTX_HOURS`）+ `ops/observability/probe-image-video-deepctx.sh`（openai 账号池/报错归属/流量出处一次性深挖），均经 run-probe 投递；只读 `row_to_json` |
| Studio 图片请求审计（Image Studio / BakeOff prompt 是否实际提交、是否同一轮、size/model 是否被前后端改写） | 机械 | `ops/observability/probe-studio-image-request-audit.sh`（经 run-probe 投递；查 `ops_system_logs component=audit.openai_image_request`；按 `WINDOW_MINUTES` / user / api_key / model / `studio_run_id` / `prompt_sha256` / request id 过滤） |
| 用户级盯盘（一组 user_id 的请求 + 错误 + 计量计费 + 图片/视频 breakout + last-seen，单次 SSM 往返，对齐 30min 汇报节奏） | 机械 | `ops/observability/probe-user-billing-watch.sh`（经 run-probe 投递；`USER_IDS` 逗号分隔整数默认 `1,16`、`WINDOW_MINUTES` 默认 30；只读 `row_to_json`，复用 probe-image-video-billing.sh 的 image/video 判别谓词） |
| Kiro 响应兼容历史（按日核对指定/全部模型的 Kiro 承接账号、stream 比例、匿名 user id、User-Agent 版本与匹配错误事件） | 机械 | `ops/observability/probe-kiro-response-compat.sh`（经 run-probe 投递；`DAYS` 默认 15、`MODEL` 默认 `claude-opus-4-8` 且 `*` 表示全部、`UA_LIMIT` 默认每日 20、`UA_FILTER` 可选；只读 `usage_logs`/`ops_error_logs`/`accounts`，不读请求或响应 body） |
| Gateway UA/TLS / usage_logs / ops / docker 指纹交叉对比（窄时间窗） | 机械 | `ops/observability/probe-gateway-ua-tls-compare.sh`（通过 run-probe.sh 投递；`WINDOW_MINUTES` 收窄 DB 窗） |
| OpenAI/Python ingress → edge OAuth mimic 出站（HTTP 头 + system，非 UA-only） | 机械 | `ops/observability/probe-oauth-mimicry-chain.sh`（edge + `PLATFORM=anthropic`）；日志 `gateway.anthropic_oauth_mimic_egress` |
| `ops_error_logs.request_body` 顶层参数形状聚合（若线上 schema 保留 body，则只输出 top-level keys / deprecated sampling key 存在性；若无 body 列则输出 schema-unavailable + 错误样本） | 机械 | `ops/observability/probe-ops-error-request-shape.sh`（经 run-probe 投递；用于确认错误请求是否携带 `temperature` / `top_p` / `top_k` 等字段，不输出 prompt/body 原文） |
| final error 与 QA evidence 覆盖率（request_id 关联、retention、blob ref/本地存在性；不输出正文或 URI） | 机械 | `ops/observability/probe-qa-error-evidence.sh`（经 run-probe 投递；先判断是否有可深挖证据，再决定是否需要隐私受控的正文检查） |
| `SUB2API_DEBUG_GATEWAY_BODY` 日志拉回本机（SSM gzip → S3 presigned PUT → 本地 gunzip） | 机械 | `ops/observability/fetch-gateway-debug-log.sh --target prod\|edge:<id>`（**本地** orchestrator，不走 run-probe） |
| anthropic capacity / cap 与 schedulable 证据 | 机械 | `ops/observability/probe-caps.sh`（已有，通过 run-probe.sh 投递）/ `ops/anthropic/manage-anthropic-config.py snapshot` |
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

## 0) 稳定性原则

1. **先识别环境，不猜容器名。** 先解析 target，再远端 `docker ps` / `docker compose ps`；不要硬编码 `tokenkey-app`、`postgres` 等旧名。当前常见容器名是 `tokenkey`、`tokenkey-postgres`、`tokenkey-redis`、`tokenkey-caddy`，但仍以 live 输出为准。
2. **先查 schema，不猜列名。** 查询新表或不确定字段前，用 `information_schema.columns` 或 `SELECT row_to_json(t) LIMIT 1` 确认可用列；避免猜 `enabled`、`tpm_limit`、`rpm_limit` 等。
3. **先 count/aggregate，再 sample。** `ops_error_logs` 先做 count/by_status/by_kind/by_minute；只有需要时再取少量样本。
4. **小输出优先。** SSM stdout 易截断；默认输出聚合，不 dump 大 body / 全日志。大结果写 `$CLAUDE_JOB_DIR` 本地或远端安全临时文件，只回传摘要和路径。
5. **UTC + 本地时间双写。** DB 与 Docker logs 统一用 UTC ISO；用户报告使用 Asia/Shanghai 时同时标注换算。
6. **区分最终失败和中间 upstream event。** `ops_error_logs.status_code` 是最终用户侧状态；`upstream_errors[*]` 可能是被重试/降级恢复的中间错误。
7. **禁止泄漏敏感信息。** 不输出 Authorization、API key、cookie、完整 request_body、OAuth token、数据库密码。必要时只输出 sanitized/truncated 摘要。
8. **一次失败先修命令，不扩大权限。** SSM/SQL 失败时先读 error、修 quoting/schema；不要改线上状态来绕过。
9. **prod 的 `upstream-429 by account` / `recovered-200` 不反映镜像 edge 死活——别拿它当 triage 主信号。** 这两个数被双重灌水：(a) 客户端断流（context canceled）在重试/failover 中途被打上残留 `upstream_status=429`；(b) `shouldFailoverUpstreamError` 对 429 也 failover，死 edge 的 429 沿 failover 链涂抹到链尾每个账号。2026-06-06 压测中 **edge-us5 实发 77 个 429，prod 却记 1266（16×）**；六个镜像 edge 的 prod upstream-429 都是 1272–1941，看着齐平，**底下从全健康（us5）到全宕 3.5h（us3：0×200）都有**。更反直觉：**`recovered-200` 越高 = edge 越死**（全靠 failover 救回，健康 edge 直接服务无需"恢复"）。判断 edge 死活的**唯一可靠口径** = edge **自身** access-log 的 `served_200 : no_available_429` 比 + 可调度账号数 → 跑 `ops/observability/scan-edge-health.sh`（见 §6.1 D）。详见 memory「判断 edge 死活看 edge 自身比值非 prod upstream-429」与 §0 上方的 `upstream_error_rate` 假 P0 记忆。

## 1) Target 解析

### 1.1 Edge target

**Edge 全部是 Lightsail**（EC2/CFN edge 路径已于 2026-06-07 退役；`deploy/aws/stage0/edge-targets.json` 仅留 `targets: {}` 空壳防工具 hard-fail，**不要**再对 edge 走 EC2 解析或查 `tokenkey-edge-<id>-stage0` CFN stack——edge 没有 CFN，prod 才有，见 §1.2）。fleet 真值源：`deploy/aws/lightsail/edge-targets-lightsail.json`，`deployable=true` 的 uk1、us2、… 可查。

```bash
python3 scripts/stage0/resolve-edge-deploy-route.py --edge-id "$EDGE_ID" --json
# → platform, workflow_file, region, domain, confirm_value
```

```bash
python3 deploy/aws/lightsail/resolve-edge-lightsail-target.py --edge-id "$EDGE_ID"
# SSM 目标：tag EdgeId=<id> + Platform=lightsail → managed instance mi-*
```

最终必须确认：`edge_id` / `deployable` / `region` / `instance_id`（`mi-*`）/ `domain`。

planned edge 默认不查；除非用户显式允许 `allow_planned=true`。

### 1.2 Prod / main gateway

prod 已固定（`api.tokenkey.dev`，AWS Graviton arm64）：**stack=`tokenkey-prod-stage0`，region=`us-east-1`**。直接解析 instance id（只读）：

```bash
REGION=us-east-1; STACK=tokenkey-prod-stage0
IID=$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue" --output text)
# 回退：若 Outputs 无 InstanceId，用 describe-stack-resources 取 AWS::EC2::Instance 的 PhysicalResourceId
```

栈若曾在 us-east-1 以外创建过，把 `REGION` 改成当时的 region（见 `deploy/aws/README.md`）。其余仍不确定的入口（新栈/新域名）才回到"先问或只查 CI、不猜实例"。

### 1.3 快速环境指纹

远端首个 SSM 只读命令只做环境指纹：

```bash
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'
docker exec tokenkey sh -c 'printenv | grep -E "^(APP_|TOKENKEY_|DATABASE_|REDIS_)" | sed -E "s/(PASSWORD|TOKEN|SECRET|KEY)=.*/\1=<redacted>/"' || true
docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -F $'\t' -c "SELECT now() AS db_now;"
```

如果容器名不同，改用 `docker ps` 结果中的实际名称。

## 2) SSM 执行方式（机械化）

所有远端命令通过 `ops/observability/run-probe.sh` 投递。包装了 base64 投递 + `aws ssm send-command` + 对同一 CommandId 持续 `get-command-invocation` 取 stdout 的标准流程；instance 解析按 target 分流——edge 委托 `ops/stage0/edge_ssm_execution.py`（Lightsail tag-SSM → `mi-*`），prod 走 CloudFormation `describe-stacks`。

```bash
# 调用 probe-caps.sh（caps + 不可调度证据 + Redis 快照 + 最近 2h ops_error_logs）
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-caps.sh \
  --env PLATFORM=anthropic \
  --env ERR_HOURS=2

# Edge 同款
bash ops/observability/run-probe.sh \
  --target edge:us1 \
  --script ops/observability/probe-caps.sh
```

> `ALLOW_PLANNED=1` 分支走已退役的 EC2 edge 矩阵（现为空 `targets: {}`），必然解析失败——planned edge 经 run-probe 已不可探，不要再用。

Exit codes：
- `0` SSM Success；wrapper stdout = remote stdout
- `1` wrapper 参数错（target/script 缺失、script 不存在）
- `2` AWS transport 失败（assume-role / send-command / describe-stacks）
- `3` 远端 SSM status ≠ Success

stderr 的 `[remote-stderr] ...` 行是远端 stderr 透传（解析输出时忽略）。失败优先看 `[run-probe] status=...` 行。不要手动重写 base64 / SSM 调用 —— 漂移点全部消化在 wrapper 内。

## 3) 时间窗口规范

始终在输出中记录：

```text
time_window_utc=<start_iso_z>..<end_iso_z>
time_window_local=<Asia/Shanghai start>..<end>
assumption=<用户未给明确时间时的假设>
```

Docker logs 使用：

```bash
docker logs tokenkey --since '<UTC_ISO_Z>' --until '<UTC_ISO_Z>'
```

PostgreSQL 使用：

```sql
WITH bounds AS (
  SELECT timestamp with time zone '<start>' AS since,
         timestamp with time zone '<end>' AS until
)
```

不要混用本地时区字符串和 DB UTC 字段。

**窄窗口 outage（如 30 分钟 VM hang）**：除 Docker `--since` 外，对 DB 侧聚合优先用 `probe-gateway-ua-tls-compare.sh` 的 `WINDOW_MINUTES`（见 §5.1），不要用大 `LIMIT` 代替时间过滤。

## 4) `ops_error_logs` 标准 triage（机械化）

聚合 SQL 由 `ops/observability/ops-error-triage.sh` 渲染（schema 探测 + summary + by_status + upstream_events + 429-by-minute），所有输出都是 `row_to_json(t)`，字段名嵌在值旁，下游用 `jq`/`json.loads` 按字段取，**禁止按列号读**。通过 §2 的 `run-probe.sh` 投递：

```bash
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/ops-error-triage.sh \
  --env WINDOW_HOURS=24 \
  --env PATH_FILTER=/v1/messages
```

env 契约（脚本顶部 docstring 是 ground truth）：

| env | 默认 | 含义 |
|---|---|---|
| `WINDOW_HOURS` | 24 | 回看小时数（正整数；非整数 fail-fast） |
| `PATH_FILTER` | （空） | `request_path` 精确过滤 |
| `MODEL_FILTER` | （空） | `requested_model` 精确过滤 |
| `STATUS_MIN` | 400 | `final_error_rows` 阈值 |
| `TOP_KIND_LIMIT` | 50 | upstream_events 行数上限 |
| `TOP_MIN_LIMIT` | 30 | by_minute_429 行数上限 |

输出分段：`=== meta ===` / `=== schema ===` / `=== summary ===` / `=== by_status ===` / `=== upstream_events ===` / `=== by_minute_429 ===`。

新增字段或新维度：先改 `ops-error-triage.sh` 再回头改本 SKILL，不直接在 prose 里写新 SQL。

> **裸数字误报坑**：`grep -c '429'` / `'529'` 会命中 UUID、`body_bytes`、`latency_ms` 子串，给出假计数。判**真实** HTTP 429/529 用 `ops-error-triage.sh` 的 `by_status` 段（权威列 `status_code` / `upstream_status_code`），或用 §5 的 access log 解析的 `markers` 字段匹配 `rate_limit_error` / `overloaded_error` 语义标记。

**解读规则（真判断）**：
- `final_status=200` + `kind=signature_error` 等 → 首轮上游错误已恢复，不算用户侧失败。
- `final_status >= 400` + 同 kind → 用户侧真实失败，需要样本。
- `GROUP_RPM_EXCEEDED` 且 `upstream_errors` 为空 → 本地分组限额挡住，非上游故障。

## 5) Docker access log 解析（机械化）

`ops/observability/parse-access-log.py` 解析 `http request completed` JSON 行（按 path/model 过滤 → status/model/minute 直方图 + latency p50/p90/p95/p99/max + marker 计数），输出单个 JSON 对象。三种输入模式（互斥）：

```bash
# A) docker logs（最常见；远端跑后管道进 parser）
docker logs tokenkey --since 1h \
  | python3 ops/observability/parse-access-log.py --stdin --path /v1/messages

# B) 本地已落地的日志文件
python3 ops/observability/parse-access-log.py \
  --file /tmp/tokenkey.log --path /v1/messages --model claude-sonnet-4-6

# C) 直接调起 docker logs（需要本地能跑 docker）
python3 ops/observability/parse-access-log.py --docker tokenkey --since 1h
```

输出 schema（脚本 docstring 是 ground truth）：

| key | 含义 |
|---|---|
| `totals` | `lines_seen / lines_parsed / lines_skipped / bad_count` |
| `status_counts` | `{"200": N, "503": N, ...}` |
| `by_minute` | `[{minute_utc, status_code, n}]`（top N 按 n desc） |
| `by_model_status` | `[{model, status_code, n}]` |
| `markers` | 默认匹配 `GROUP_RPM_EXCEEDED / thinking block ... / rate_limit_error / overloaded_error / 529 / timeout / no available accounts` 等；可用 `--markers` 覆盖 |
| `latency_ms` | `{n, p50, p90, p95, p99, max}` 或 `null` |

确定性保证：同一份 stdin + 同一组 flag → 字节一致的 JSON（排序稳定、整数 ms、无 locale 浮点）。

## 5.1) Gateway UA/TLS 指纹对比（机械化）

`ops/observability/probe-gateway-ua-tls-compare.sh` 在同一 target 上交叉采样 **usage_logs**（含 TLS profile join）、**ops_system_logs**（gateway/http.access 组件）、**docker access log**（`http request completed` + UA/TLS 关键词行）。输出单个紧凑 JSON（SSM stdout ~24KiB 上限）；远端另写 `/tmp/tk-gateway-ua-tls-full.json` 供 SSH 拉全量（本 skill 默认不 dump）。

通过 §2 `run-probe.sh` 投递：

```bash
# 默认：usage_logs LIMIT=500 + docker logs 48h + ops 48h
bash ops/observability/run-probe.sh \
  --target edge:uk1 \
  --script ops/observability/probe-gateway-ua-tls-compare.sh \
  --timeout-seconds 180

# 窄时间窗（推荐：用户给了具体 outage 区间，如「07:20–07:36 UTC」≈ 20min）
bash ops/observability/run-probe.sh \
  --target edge:uk1 \
  --script ops/observability/probe-gateway-ua-tls-compare.sh \
  --env WINDOW_MINUTES=30 \
  --timeout-seconds 180
```

env 契约（脚本 header 是 ground truth）：

| env | 默认 | 含义 |
|---|---|---|
| `LIMIT` | 500 | `usage_logs` 行数上限（`ORDER BY ul.id DESC`；与 `WINDOW_MINUTES` 叠加时先时间过滤再 limit） |
| `SINCE` | 48h | `docker logs tokenkey --since` 窗口 |
| `WINDOW_MINUTES` | （空） | 正整数时：`usage_logs` 与 `ops_system_logs` 仅查 `now() - interval 'N minutes'` |
| `CONTAINER` | auto | Docker 容器名；auto 按 active-color 解析 tokenkey-blue/green，再回退 legacy tokenkey |

输出 schema 要点：

| key | 含义 |
|---|---|
| `meta.window_minutes` | 生效的 DB 窄窗（未设则为 `null`） |
| `usage_logs.summary` | UA/TLS/client_ip 计数与 gateway 样本 |
| `ops_system_logs` | ops 侧 UA/TLS 相关字段聚合 |
| `docker_access` | access log completed 行 summary + docker 错误 |
| `docker_ua_tls_lines` | 含 UA/TLS 关键词的 docker 行样本（尾部截断） |

**何时用**：edge 间 UA/TLS 行为差异、OAuth TLS fingerprint 是否生效、outage 窗口内 gateway 是否仍有 completed 行、与 §5 access log 解析互补（本 probe 偏 cross-table 指纹，parse-access-log 偏 status/latency 直方图）。

## 5.1.1) Studio 图片请求审计（机械化）

用户反馈 Image Studio / BakeOff 图片结果严重不符合 prompt、怀疑 prompt 被改写、同一轮对比里某个模型提交了旧 prompt，或报错截图只显示 `Failed to fetch` / `Upstream request failed` 时，先用本 probe 固定证据。它查的是后端写入 `ops_system_logs` 的 `audit.openai_image_request` 行，覆盖：

- `/v1/images/generations`：Imagen / Seedream / Grok 等 OpenAI-compatible image 入口，`surface=images.generations`。
- `/v1/chat/completions`：Studio 走 Gemini-native 图片的 chat 入口，只有带 Studio image trace header 时记录，`surface=chat.completions`。

```bash
# 最近 60 分钟 Studio 图片请求审计（默认 LIMIT=20）
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-studio-image-request-audit.sh \
  --env WINDOW_MINUTES=60 \
  --timeout-seconds 120

# 用户刚反馈且知道 user/api key/model：收窄到具体人和模型
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-studio-image-request-audit.sh \
  --env WINDOW_MINUTES=30 \
  --env USER_ID=1 \
  --env MODEL=imagen-4.0-generate-001 \
  --timeout-seconds 120

# 用户截图/前端 console 拿到了同一轮 BakeOff run id：按轮次查
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-studio-image-request-audit.sh \
  --env STUDIO_RUN_ID=studio-bakeoff-image-... \
  --env LIMIT=40 \
  --timeout-seconds 120

# 已知某个 prompt hash：查是否还有其他模型/轮次提交了同一 prompt
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-studio-image-request-audit.sh \
  --env PROMPT_SHA256=<64-hex> \
  --env WINDOW_MINUTES=360 \
  --timeout-seconds 120
```

env 契约（脚本 header 是 ground truth）：

| env | 默认 | 含义 |
|---|---|---|
| `WINDOW_MINUTES` | 60 | 回看分钟数，正整数；只影响窗口内分段，`last-seen with filters` 会忽略窗口给空窗兜底。 |
| `LIMIT` | 20 | `samples` 行数上限。 |
| `USER_ID` / `API_KEY_ID` / `ACCOUNT_ID` | （空） | 精确过滤身份或账号；整数。 |
| `MODEL` | （空） | 同时匹配 top-level `model`、`extra.requested_model`、`extra.forward_model`。 |
| `STUDIO_SOURCE` | （空） | `studio.image` 或 `studio.bakeoff.image`。 |
| `STUDIO_RUN_ID` / `STUDIO_PANEL_ID` | （空） | 前端 trace header；BakeOff 同一次点击应共享一个 `studio_run_id`，panel 通常是模型 id。 |
| `PROMPT_SHA256` | （空） | prompt 原文 SHA-256；用于比较，不输出完整 prompt。 |
| `REQUEST_ID` / `CLIENT_REQUEST_ID` | （空） | 与 `ops_error_logs`、Docker access log、usage_logs 交叉定位。 |

输出解读：

| 分段 | 用途 |
|---|---|
| `summary` | 看窗口内是否有 audit 行、涉及几个用户/key/run/prompt hash。 |
| `by source/model/size` | 直接看 `requested_model`/`forward_model`、`size`/`forward_size`、入口 `surface` 是否符合预期。 |
| `by studio run` | BakeOff 一次点击的模型集合、panel 数、prompt hash 数；同一轮 `prompt_hashes > 1` 是强信号：前端实际提交了不同 prompt。 |
| `prompt consistency by run` | 同一 `studio_run_id` 下逐个 prompt hash 展开，带 200 字节 preview；用来判断“旧 prompt/页面状态错乱” vs “同 prompt 但模型输出不佳”。 |
| `samples` | 少量样本，含 `prompt_preview`（已脱敏且最多 1024 bytes）、`prompt_sha256`、`prompt_bytes`/`prompt_runes`、body hash、size/model、Studio trace。 |
| `related ops_error_logs by request_id` | 同窗口内按 request id 关联真实错误；若为空，不代表请求成功，只代表没有匹配到 error row。 |
| `last-seen with filters` | 空窗口时判断是“确实没打到审计点”还是时间窗太窄。 |

判断纪律：
- `prompt_preview` 只是脱敏截断预览，不能当完整 prompt；严格比较用 `prompt_sha256` + `prompt_bytes`/`prompt_runes`。
- 同一 `studio_run_id`、不同模型的 `prompt_sha256` 一致：当前证据不支持“后端改了 prompt”；继续看 `size` / `forward_size` / `forward_model` / upstream error。
- 同一 `studio_run_id` 出现多个 `prompt_sha256`：优先怀疑前端提交旧状态、页面 hydrate/缓存错乱、或用户看的不是同一轮结果；再用 `studio_panel_id` 和 `request_id` 定位具体 panel。
- `requested_model != forward_model` 或 `size != forward_size`：说明后端确实改写了转发字段，必须回到 handler / mapper 查为什么。
- 查不到 audit 行：只说明当前部署/窗口/入口没有该审计证据；旧事故仍只能靠 `usage_logs`、`ops_error_logs`、Docker access log、以及已开启时的 §5.2 gateway debug body 推断，不能反推“prompt 没被改”。

## 5.2) Debug gateway body 日志拉回本机（机械化，本地 orchestrator）

当容器已开启 `SUB2API_DEBUG_GATEWAY_BODY`（写入 `gateway_debug.log`，见 `gateway_service.go`）且需要**完整 request/response body** 做 deep triage 时使用。**不在 SSM stdout 里传输**——走 gzip + presigned S3 PUT + 本机 `aws s3 cp` + gunzip。

```bash
bash ops/observability/fetch-gateway-debug-log.sh --target edge:uk1
bash ops/observability/fetch-gateway-debug-log.sh --target prod --out "$CLAUDE_JOB_DIR/gateway-debug"
```

CLI 契约：

| 参数 / env | 默认 | 含义 |
|---|---|---|
| `--target` | （必填） | `prod` 或 `edge:<id>`（edge 经 `ops/stage0/edge_ssm_execution.py` 解析为 `mi-*`；prod 走 CFN） |
| `--out` / `OUT_DIR` | `./.cache/gateway-debug` | 本机输出目录 |
| `--log-path` / `LOG_PATH` | `/app/data/gateway_debug.log` | 容器内 debug 文件路径 |
| `SSM_OUTPUT_S3_BUCKET` | layer-zip-repro-… | 临时对象桶（上传后脚本会 `aws s3 rm` 清理 key） |
| `AWS_SSM_WAIT_MAX` | 900 | SSM 等待秒数 |

成功时 stdout 最后一行是本地 `.log` 绝对路径；在本地用 `grep`/`jq`/`less` 分析，**报告里仍遵守 §0 脱敏**（不粘贴 Authorization、完整 API key、OAuth token）。

**前置条件**：目标实例上 debug  env 已开启且文件存在；否则 SSM 会在 `docker exec tokenkey test -f …` 失败。未开启时先用 §5 / §5.1 聚合，或经 deploy/ops 流程临时开启（超出本 skill 只读边界，需显式确认）。

**与 run-probe 的分工**：`run-probe.sh` 只回传远端 stdout 字节；本脚本需要本机 boto3 presign + S3 下载，因此是**开发者机器上运行的 orchestrator**，不要塞进 run-probe。

## 5.3) Admin UI 性能探测（机械化）

Admin 页面慢、首屏资源异常、或发版后需要复测 `/admin/*` 运营体验时，用两个只读 probe 组合：access-log 聚合看真实访问路径和慢样本，API timing 主动测每个页面依赖的接口形状。都通过 §2 `run-probe.sh` 投递；脚本只读 Docker logs、settings 表里的 `admin_api_key`，并只调用 `GET` 与只读 batch `POST`，不跑 test/run/export/sync/reset 类 mutating endpoint。

```bash
# 真实访问日志聚合：/api/v1/admin/* endpoint latency + /admin 前端资源 preload/慢资源样本
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-admin-ui-perf.sh \
  --env SINCE=24h \
  --timeout-seconds 180

# 主动接口 timing：Dashboard / Usage / Accounts / Groups / Ops / Payment / Risk 等页面形状
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-admin-ui-api-timing.sh \
  --timeout-seconds 240

# 聚合运行态：确认 dashboard aggregation env/config、watermark、model/group backfill marker
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-admin-aggregation-config.sh \
  --timeout-seconds 120

# 针对 dashboard/models 冷态慢：验证 model daily rollup 是否生效、raw today group-by 耗时
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-admin-model-rollup-timing.sh \
  --timeout-seconds 240

# 针对 usage/dashboard group distribution 冷态慢：验证 group daily rollup 是否生效
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-admin-group-rollup-timing.sh \
  --timeout-seconds 240
```

输出解读：

| probe | 主要字段 | 用途 |
|---|---|---|
| `probe-admin-ui-perf.sh` | `admin_by_endpoint_top`、`frontend_by_path_top`、`slow_admin_samples`、`slow_frontend_samples` | 找出真实用户访问中的慢 endpoint、旧资源是否仍被 preload、慢资源样本 |
| `probe-admin-ui-api-timing.sh` | `top_slow`、`non_2xx`、`results[].label`、`curl.ttfb`、`curl.total`、`curl.size` | 主动复现各 admin 页面依赖接口的 TTFB/total，定位 stats/model/group/users-trend 等慢段 |
| `probe-admin-aggregation-config.sh` | `env_dashboard_aggregation`、`aggregation_watermark`、`hourly_min/max`、`group_daily_backfilled`、`group_daily_metrics_backfilled`、`model_daily_backfilled` | 部署前后确认聚合运行态、历史 backfill marker 是否已启用对应快路径 |
| `probe-admin-model-rollup-timing.sh` | `model_daily_backfilled`、`raw_7d_model_group_explain`、`rollup_completed_days_explain`、`raw_today_model_group_explain`、`raw_vs_rollup_plus_today_delta` | 验证 dashboard model distribution 的 7d raw group-by 耗时、completed-day rollup 耗时、today raw tail 耗时、历史 backfill marker 与快路径一致性 |
| `probe-admin-group-rollup-timing.sh` | `raw_7d_group_explain`、`rollup_completed_days_explain`、`raw_today_group_explain`、`raw_vs_rollup_completed_days_delta` | 验证 Dashboard/Usage group distribution 的 7d raw group-by 耗时、completed-day rollup 耗时、today raw tail 耗时和历史一致性 |

`probe-admin-model-rollup-timing.sh` / `probe-admin-group-rollup-timing.sh` 默认只输出 meta、窗口计数与一致性 delta，避免 SSM stdout 被 `EXPLAIN FORMAT JSON` 截断；需要执行计划时追加 `--env INCLUDE_EXPLAIN=1`，或用 `--env INCLUDE_EXPLAIN=1 --env EXPLAIN_SECTION=raw_7d|rollup_completed_days|raw_today` 单段拉取。

解读纪律：
- Dashboard/Usage 的 combined snapshot 可能被前一个 split 请求预热；需要冷态判断时，把 split 测点排在 combined 前，或比较 `trend-only` / `models-only` / `groups-only` 的首个耗时。
- `http_code=500` 先看具体 label，不要把整个 admin UI 判死；例如 `dashboard.snapshot.*users-trend*` 失败通常只影响 users-trend 区块。
- API timing 的公网网络耗时很小，`ttfb≈total` 时基本就是服务端/DB/缓存路径耗时；DNS/connect 异常才看网络。

## 6) 配置与限额核对

### 6.1 Anthropic OAuth / Edge capacity（机械化）

不在本 skill 里重复手写 SQL 查 cap 字段——会与 traffic-profile / anthropic-oauth-config 的真值源漂移。按调用面分工：

```bash
# A) 单账号 stability tier baseline 核对（最常用）
python3 ops/anthropic/check-edge-oauth-stability.py \
  --edge-id "$EDGE_ID" --account-name all --json

# B) 跨 edge + prod 完整 snapshot（含 cap、schedulable、temp_unschedulable_reason、stub pool_mode）
python3 ops/anthropic/manage-anthropic-config.py snapshot --out "$CLAUDE_JOB_DIR/snap.json"

# C) 仅排障：caps + 不可调度证据 + Redis 快照 + 近 2h ops_error_logs（远端跑 probe-caps.sh）
bash ops/observability/run-probe.sh \
  --target edge:"$EDGE_ID" \
  --script ops/observability/probe-caps.sh \
  --env PLATFORM=anthropic
```

```bash
# D) 镜像 edge 死活/容量 fleet 横扫（判断"哪个 edge 真在服务" vs "被 prod failover 掩盖的死 edge"）
#    —— 镜像 edge 事件（cc-<edge> 账号 error/cooldown，或"某 edge 像挂了"）应 *先* 跑这一步，再钻 ops_error_logs。
bash ops/observability/scan-edge-health.sh                          # 全 deployable edge
bash ops/observability/scan-edge-health.sh --with-prod --since 15h  # 含 prod + 加宽窗口（压测/事后复盘）
bash ops/observability/scan-edge-health.sh --edges us3,us6          # 子集
```

输出一张按严重度排序的表：每 edge `schedulable_accounts / served_200 / no_available_429 / ratio / wait_timeout / SPOF / verdict(healthy|thin|idle-thin|degraded|down|idle|no-accounts)`，并附 ACTION（down/degraded/unreachable）、RISK（单账号 SPOF）与 PLAN（no-accounts：0 账号且无被拒流量，补号或下线的态势待办，不算事故）摘要。**判定来自 edge 自身 access-log，不受 §0 trap 9 的 prod upstream-429 污染。** verdict 逻辑在纯函数 `edge_health_verdict.py`（`--selftest` fixtures 钉死六个真 edge + 边界，已进 preflight），阈值 + rationale 在 `edge-health-thresholds.json`。

`group.rpm_limit` 与 `user_group_rate_multipliers` 由 admin UI 维护，不由本 skill 派生（按 memory「group.rpm_limit 与 account 字段独立」）；如需读 group 状态请直接看 admin UI，不要在 SKILL 里写漂移容易的 SELECT。

### 6.2 解释规则

- `GROUP_RPM_EXCEEDED` 且 upstream events 为空：本地分组限额挡住，非上游故障。
- upstream 429/529 + final 429/5xx：上游或账号容量问题。
- final 200 + upstream error events：恢复成功，ops 展示需降噪，不应等同用户失败。
- 单 CLI 窗口也可能有多模型/并发短峰，必须看 access log 实际 RPM，而不是凭窗口数量判断。

## 7) CI / deploy 日志排障

GitHub 相关一律用 `gh`，不要猜 URL。

PR checks：

```bash
gh pr view <PR> --json title,headRefName,baseRefName,state,mergeStateStatus,url
gh pr checks <PR> --watch=false
```

Run 定位：

```bash
gh run list --branch <branch> --limit 20 --json databaseId,workflowName,displayTitle,status,conclusion,createdAt,headSha
gh run view <run_id> --json jobs,conclusion,status,url
gh run view <run_id> --log-failed
```

Deploy/SSM workflow：先定位 workflow run、job，再读失败 job log。不要从本地猜 CloudFormation/SSM 命令是否执行。

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

## 8.5) 发版后核验：按改动集查日志信号（模板：upstream-issue-remediation / PR #455）

发版滚动后，按「每个修复 → 一个固定日志 marker」核验是否生效、有无异常。Target 解析见 §1，SSM 执行见 §2；默认查 **prod main gateway**（这批默认开启项主要落在 Anthropic 主力路径），edge 同款换 target。机械化：marker 是固定串，直接 `grep -F` 计数，不拼 SQL、不靠记忆。

```bash
# 统一取一段窗口的 docker logs（6h 示例），多次 grep 复用
docker logs tokenkey --since 6h > /tmp/tk.log 2>&1

# #2608 客户端诱发 400 跳过账号惩罚（Anthropic 主力）。预期：与畸形请求量相当；
#        且不应再出现「健康账号被 invalid_request 400 冷却」。
grep -Fc 'anthropic_client_induced_400_skip_penalty' /tmp/tk.log

# #1833/#1544 无定价模型零计费告警。它会“暴露”当前正在免费跑的模型——
#        据此配渠道定价才是真正止血（本修复只让泄漏可见，不改扣费金额）。
grep -F 'gateway_usage.pricing_missing_record_zero_cost' /tmp/tk.log \
  | grep -oE '"billing_model":"[^"]*"' | sort | uniq -c | sort -rn
grep -Fc 'gateway_usage.cost_calculation_failed_record_zero_cost' /tmp/tk.log   # 非 pricing-missing 计算失败，应≈0

# #2727 隐式限流跨请求 bench（opt-in；默认关 → 应为 0，除非已设 >0）
grep -Fc 'openai_implicit_throttle_cooldown_applied' /tmp/tk.log
grep -Fc 'openai_implicit_throttle_cooldown_failed'  /tmp/tk.log    # SetTempUnschedulable 失败，应为 0

# #1981 限流 reset 钳制（DEFAULT-ON；未显式设 0 即生效，默认 ceiling 18000s/5h）。
#        marker 仅在超长 upstream reset 被截断时出现；不能用 marker=0 推断「仍关闭」。
grep -F 'openai_rate_limit_reset_clamped' /tmp/tk.log \
  | grep -oE '"original_reset":"[^"]*"' | head
grep -F 'anthropic_rate_limit_reset_clamped' /tmp/tk.log \
  | grep -oE '"original_reset":"[^"]*"' | head
```

无独立 marker、需间接核验的两项：

| 修复 | 为何无专用日志 | 间接核验 |
|---|---|---|
| **#1934** sticky 切组 | 复用既有 sticky 删除路径，无新行 | OpenAI/newapi sticky failover 率 / `decision.Layer` 分布有无异常上升（§5 parse-access-log by-minute）；预期几乎无变化（仅平台 openai/newapi，不碰 Anthropic） |
| **#1946** 监控 thinking-block | 提取层改动，无新行 | Anthropic 渠道监控「测试失败」误报是否下降（渠道监控结果 / ops_error_logs 里 challenge mismatch 计数应降） |

opt-in 开关确认（仅 #2727 默认关；#1981 clamp 为 DEFAULT-ON，见上节 grep）：

- `#2727` `tk_openai_implicit_throttle_cooldown_seconds`：上面 marker 计数为 0 即证明仍默认关；要启用先设 SettingKey 再复查 marker 出现。
- `#1981` `tk_openai_max_rate_limit_cooldown_seconds` / `tk_anthropic_max_rate_limit_cooldown_seconds`：未写入 settings 或非法值时走代码默认 18000s；显式 `"0"` 关闭 clamp。DB 显式值（如 `"3600"`）优先于代码默认。

> 按 §0「配置收敛≠线上稳定」：marker 计数只证明代码路径被走到，真实稳定仍需对照 final `status_code`、503 率、账号可调度数（§4 / §5 / §6）。本节 marker 串以 `backend/internal/service/*` 的 `slog`/`zap` 调用为 ground truth；改了日志文案需同步本节。

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
