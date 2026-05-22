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

## 2) SSM 执行方式

所有远端命令用 `aws ssm send-command` + `get-command-invocation`。本地临时 JSON 放 `$CLAUDE_JOB_DIR`，不要用 `/tmp`。

推荐参数文件形态：

```json
{
  "commands": [
    "set -euo pipefail",
    "echo 'diagnostic start'",
    "docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'"
  ]
}
```

执行：

```bash
cmd_id=$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --comment "Claude read-only TokenKey troubleshooting" \
  --parameters "file://$CLAUDE_JOB_DIR/<name>.json" \
  --query 'Command.CommandId' --output text)
aws ssm wait command-executed --region "$REGION" --command-id "$cmd_id" --instance-id "$INSTANCE_ID"
aws ssm get-command-invocation --region "$REGION" --command-id "$cmd_id" --instance-id "$INSTANCE_ID" --output json
```

失败处理：
- 先读 `StandardErrorContent` 和 `ResponseCode`。
- 如果 stdout 为空但 success，通常是 heredoc/JSON quoting 没展开；改成 `psql -c` 或远端 Python 包装。
- 如果输出截断，缩小 SQL、只返回聚合，或把远端输出写文件后 `tail`/`wc`/摘要。

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

## 4) `ops_error_logs` 标准 triage

### 4.1 先确认 schema

```sql
SELECT column_name, data_type
FROM information_schema.columns
WHERE table_schema='public' AND table_name='ops_error_logs'
ORDER BY ordinal_position;
```

必要时用：

```sql
SELECT row_to_json(l) FROM ops_error_logs l ORDER BY created_at DESC LIMIT 1;
```

### 4.2 最小聚合 SQL

根据实际列名调整；默认 PostgreSQL only。

```sql
WITH bounds AS (
  SELECT timestamp with time zone '<start>' AS since,
         timestamp with time zone '<end>' AS until
), base AS (
  SELECT l.*
  FROM ops_error_logs l, bounds b
  WHERE l.created_at >= b.since AND l.created_at < b.until
    AND ('<path>' = '' OR l.request_path = '<path>')
    AND ('<model>' = '' OR l.requested_model = '<model>')
)
SELECT 'summary' AS section,
       COUNT(*) AS total_ops_rows,
       COUNT(*) FILTER (WHERE status_code >= 400) AS final_error_rows,
       MIN(created_at) AS first_at,
       MAX(created_at) AS last_at
FROM base;
```

```sql
WITH bounds AS (...), base AS (...)
SELECT 'by_status' AS section, status_code, COUNT(*) AS n,
       MIN(created_at) AS first_at, MAX(created_at) AS last_at
FROM base
GROUP BY status_code
ORDER BY n DESC, status_code;
```

```sql
WITH bounds AS (...), base AS (...), events AS (
  SELECT l.id AS log_id, l.created_at, l.status_code,
         e.value AS ev
  FROM base l
  CROSS JOIN LATERAL jsonb_array_elements(COALESCE(l.upstream_errors, '[]'::jsonb)) AS e(value)
)
SELECT 'upstream_events' AS section,
       COALESCE(ev->>'kind','') AS kind,
       COALESCE(ev->>'platform','') AS platform,
       COALESCE(ev->>'account_id','') AS account_id,
       COALESCE(ev->>'account_name','') AS account_name,
       COALESCE(ev->>'upstream_status_code','') AS upstream_status,
       status_code AS final_status,
       COUNT(*) AS n,
       MIN(created_at) AS first_at,
       MAX(created_at) AS last_at
FROM events
GROUP BY kind, platform, account_id, account_name, upstream_status, final_status
ORDER BY n DESC, kind, final_status
LIMIT 50;
```

### 4.3 常用专项聚合

**429 / RPM：**

```sql
WITH bounds AS (...), base AS (...)
SELECT date_trunc('minute', created_at) AS minute,
       status_code,
       COUNT(*) AS n
FROM base
WHERE status_code = 429
GROUP BY minute, status_code
ORDER BY n DESC, minute
LIMIT 30;
```

同时查本地应用日志：

```bash
docker logs tokenkey --since '<start_z>' --until '<end_z>' 2>&1 \
  | grep -E 'GROUP_RPM_EXCEEDED|USER_RPM|rate limit|429|529|overload|temp.*unsched' \
  | tail -n 200 || true
```

> **裸数字误报坑（实测）**：`grep -c '429'` / `'529'` 会命中 request_id/UUID、`body_bytes`、`latency_ms`、`content_len` 里的子串，给出几十上百的**假计数**。判**真实** HTTP 429/529 要么解析 JSON 看 `status_code` 字段，要么匹配语义标记 `rate_limit_error` / `overloaded_error` / `too_many_requests`，**不要数裸的 429/529**。`ops_error_logs` 的 `status_code` / `upstream_status_code` 列才是权威，应优先用 §4.2 的 SQL 聚合而非 grep 数字。

**Anthropic thinking signature：**

```sql
WITH bounds AS (...), base AS (...), events AS (...)
SELECT date_trunc('hour', created_at) AS hour,
       status_code AS final_status,
       COUNT(*) AS n
FROM events
WHERE ev->>'kind' = 'signature_error'
   OR lower(COALESCE(ev->>'message','') || ' ' || COALESCE(ev->>'detail','')) LIKE '%signature%'
GROUP BY hour, final_status
ORDER BY hour, final_status;
```

解释规则：
- `final_status=200` + `kind=signature_error` = 首轮上游 400 后已恢复。
- `final_status>=400` = 用户侧真实失败，需要样本。

**耗时：** 如果表没有 `duration_ms`，不要继续猜；改用 access log `latency_ms`。

```sql
WITH bounds AS (...), base AS (...)
SELECT COUNT(*) AS n,
       percentile_disc(0.50) WITHIN GROUP (ORDER BY duration_ms) AS p50_ms,
       percentile_disc(0.90) WITHIN GROUP (ORDER BY duration_ms) AS p90_ms,
       percentile_disc(0.95) WITHIN GROUP (ORDER BY duration_ms) AS p95_ms,
       percentile_disc(0.99) WITHIN GROUP (ORDER BY duration_ms) AS p99_ms,
       MAX(duration_ms) AS max_ms
FROM base
WHERE duration_ms IS NOT NULL;
```

## 5) Docker access log 解析

当 `ops_error_logs` 只记录异常或缺少 latency 时，用容器 JSON log 解析 `http request completed`。

推荐远端 Python 包装，避免 grep/awk quoting 地狱：

```bash
python3 - <<'PY'
import json, re, subprocess, collections
START='<start_z>'; END='<end_z>'; PATH_FILTER='<path>'
cmd=['docker','logs','tokenkey','--since',START,'--until',END]
p=subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, check=False)
status=collections.Counter(); model_status=collections.Counter(); minute_status=collections.Counter(); latencies=[]
markers=collections.Counter()
for line in p.stdout.splitlines():
    for k in ['GROUP_RPM_EXCEEDED','thinking blocks have invalid signature','thinking block retry succeeded','429','529','timeout']:
        if k in line: markers[k]+=1
    if 'http request completed' not in line:
        continue
    m=re.search(r'\{.*\}$', line)
    if not m:
        continue
    try: obj=json.loads(m.group(0))
    except Exception: continue
    if PATH_FILTER and obj.get('path') != PATH_FILTER:
        continue
    sc=obj.get('status_code'); model=obj.get('model') or ''; ts=obj.get('completed_at') or ''
    if sc is None: continue
    status[sc]+=1; model_status[(model,sc)]+=1; minute_status[(ts[:16],sc)]+=1
    if isinstance(obj.get('latency_ms'), (int,float)): latencies.append(int(obj['latency_ms']))
print('status_counts', dict(sorted(status.items())))
print('markers', dict(markers))
print('top_minutes')
for (minute, sc), n in sorted(minute_status.items(), key=lambda kv: (-kv[1], kv[0]))[:30]: print(minute, sc, n)
print('model_status')
for (model, sc), n in sorted(model_status.items(), key=lambda kv: (-kv[1], kv[0]))[:30]: print(model, sc, n)
if latencies:
    latencies.sort()
    def pct(p): return latencies[min(len(latencies)-1, int((len(latencies)-1)*p))]
    print('latency_ms', 'n', len(latencies), 'p50', pct(.5), 'p90', pct(.9), 'p95', pct(.95), 'p99', pct(.99), 'max', latencies[-1])
PY
```

## 6) 配置与限额核对

### 6.1 Anthropic OAuth / Edge capacity

优先复用专用 skill / 脚本：

```bash
python3 ops/anthropic/check-edge-oauth-stability.py \
  --edge-id "$EDGE_ID" \
  --account-name all \
  --json
```

需要查 live DB 时只读：

```sql
SELECT id, name, platform, type, status, schedulable,
       concurrency, priority, channel_type,
       extra->>'stability_tier' AS stability_tier,
       extra->>'base_rpm' AS base_rpm,
       extra->>'max_sessions' AS max_sessions,
       extra->>'window_cost_limit' AS window_cost_limit
FROM accounts
WHERE platform='anthropic'
ORDER BY id;
```

分组列名可能演化，先查 schema；当前常见：

```sql
SELECT id, name, platform, status, rpm_limit, sticky_routing_mode
FROM groups
WHERE deleted_at IS NULL
ORDER BY id;
```

用户/分组 override：

```sql
SELECT user_id, group_id, rate_multiplier, rpm_override
FROM user_group_rate_multipliers
ORDER BY user_id, group_id;
```

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
