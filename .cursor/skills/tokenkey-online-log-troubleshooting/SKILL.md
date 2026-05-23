---
name: tokenkey-online-log-troubleshooting
description: >-
  Read-only TokenKey production/edge troubleshooting workflow for querying live
  logs, ops_error_logs, Docker containers, SSM targets, CI/deploy runs, and
  turning evidence into a stable root-cause summary without ad-hoc command
  guessing.
---

# TokenKey：线上日志查询与问题定位

适用于本仓库（TokenKey fork of sub2api）的 prod / edge Stage0 线上排障。目标是把“识别环境 → 只读采样 → 聚合证据 → 定位根因 → 给出最小动作建议”固定成稳定流程，避免每次临时猜容器名、SQL 字段、SSM 参数或时间窗口。

权威纪律以仓库根 `CLAUDE.md` 为准；本 skill 默认**只读**。任何写线上配置、重启容器、部署、删数据、改分支或发 PR comment 都必须另行显式确认。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。这张表是未来 PR 编辑本 skill 时 reviewer 的核对抓手：新增「步骤」必须先按此分类。

| 步骤 | 类型 | 承载 |
|---|---|---|
| 解析 prod / edge target（region / instance_id / domain） | 机械 | `deploy/aws/stage0/resolve-edge-target.py --edge-id <id>` + `aws cloudformation describe-stacks` |
| SSM base64 投递 + send-command + poll | 机械 | `ops/observability/run-probe.sh --target prod\|edge:<id> --script <probe.sh>` |
| `ops_error_logs` 标准聚合（schema + by_status + upstream_events + 429-by-minute） | 机械 | `ops/observability/ops-error-triage.sh`（通过 run-probe.sh 投递） |
| Docker access log 解析（status/model/minute/latency 直方图 + marker 计数） | 机械 | `ops/observability/parse-access-log.py --stdin\|--file\|--docker` |
| anthropic capacity / cap 与 schedulable 证据 | 机械 | `ops/observability/probe-caps.sh`（已有，通过 run-probe.sh 投递）/ `ops/anthropic/manage-anthropic-config.py snapshot` |
| 时间窗规范（UTC ↔ Asia/Shanghai 双写） | 判断 | prompt（含报告口径，无机械抓手） |
| 解读规则：final_status vs upstream events、镜像账号链式失败 | 判断 | prompt（架构判断，§0 列出 8 个 trap 已固化） |
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
| `scope` | 默认 `all`；可收窄到 `gateway`、`ops`、`deploy`、`ci`、`db`。 |
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

## 1) Target 解析

### 1.1 Edge target

Edge 矩阵权威文件：`deploy/aws/stage0/edge-targets.json`。

优先用脚本解析（输出是 `key=value` 行，**没有 `--json` flag**——别加，会 `unrecognized arguments` 报错；要结构化就直接读 `edge-targets.json`）：

```bash
python3 deploy/aws/stage0/resolve-edge-target.py --edge-id "$EDGE_ID"
# 加 --allow-planned 才会解析 deployable:false 的 planned edge
```

脚本给出：`edge_id` `deployable` `region` `domain` `ssm_prefix` `stack` 等。**注意脚本不输出 `instance_id`**——和 prod 一样要从 CloudFormation 单独取（同 §1.2）：

```bash
REGION=<上面解析出的 region>; STACK=<上面解析出的 stack>
IID=$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue" --output text)
# 回退：若 Outputs 无 InstanceId，用 describe-stack-resources 取 AWS::EC2::Instance 的 PhysicalResourceId
```

最终必须确认：`edge_id` / `deployable` / `region` / `instance_id` / `domain` / `ssm_prefix` / `stack`。

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

所有远端命令通过 `ops/observability/run-probe.sh` 投递。包装了 base64 投递 + `aws ssm send-command` + waiter + `get-command-invocation` 取 stdout 的标准流程，并把 region/instance 解析委托给 `resolve-edge-target.py` + `describe-stacks`。

```bash
# 调用 probe-caps.sh（caps + 不可调度证据 + Redis 快照 + 最近 2h ops_error_logs）
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-caps.sh \
  --env PLATFORM=anthropic \
  --env ERR_HOURS=2

# Edge 同款；带规划态需 ALLOW_PLANNED=1
ALLOW_PLANNED=1 bash ops/observability/run-probe.sh \
  --target edge:us1 \
  --script ops/observability/probe-caps.sh
```

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

## 10) 交接给修复流程

本 skill 只负责稳定定位。需要改配置/代码时：
- 线上配置：先输出 plan + 固定确认口令；优先调用对应专用 skill（如 Anthropic OAuth 配置）。
- 代码修复：进入正常开发流程，先建任务/必要时 plan mode，改完跑 `scripts/preflight.sh`。
- 部署/rollout：调用 Stage0 release/deploy 专用 skill，不在本 skill 内临时执行。
