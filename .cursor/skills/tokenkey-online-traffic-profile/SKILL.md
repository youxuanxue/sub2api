---
name: tokenkey-online-traffic-profile
description: >-
  Read-only TokenKey production/edge traffic-profiling workflow. Reconstructs
  per-minute request-traffic series for the past N hours per account — base RPM
  (request-start minute), sticky vs non-sticky (load-balance) RPM split, active
  sessions (idle-window), and peak concurrency — then compares each against its
  cap (base_rpm / rpm_sticky_buffer / max_sessions / concurrency) and flags
  which limit is being touched. Use when asked to profile online traffic, see
  per-minute RPM/session/concurrency, validate the admin account-card gauges
  (concurrency 1/8, $/window cost, sessions 16/30, RPM 3/28), or explain "no
  available accounts" / throttling without ad-hoc command guessing.
---

# TokenKey：线上请求流量画像（逐分钟 RPM / sticky / session / concurrency）

把"过去 N 小时某账号/edge 的流量与限额命中情况"固定成稳定的**只读**重建流程。专治：流量趋势、admin 账号卡片四个 gauge 的核对、`no available accounts` / 节流的归因。

权威纪律以仓库根 `CLAUDE.md` 为准。本 skill **只读**：只跑 `docker logs` / `psql SELECT` / `redis-cli` 读命令 / `aws ... describe|get|send-command(只读脚本)`。任何写配置、改 `max_sessions`/`base_rpm`、重启、部署都必须另行显式确认，并交给写入面 skill（`tokenkey-anthropic-oauth-config` 等）。

环境识别（prod/edge 实例解析、容器名、SSM 执行、UTC+本地双写、小输出优先）与 `tokenkey-online-log-troubleshooting` 完全一致——本 skill 复用它的 §1/§2/§3，不重复；下面只写流量画像特有的部分。

## 调用参数

```text
/tokenkey-online-traffic-profile target=<prod|edge:<id>|all-edges|domain> [hours=<N，默认1>] [minutes=<M>] [account=<id|name|all，默认all>] [model=<name>] [path=/v1/messages] [bucket=minute|5min] [allow_planned=false]
```

| 参数 | 语义 |
|---|---|
| `target` | `prod`、`edge:us1`/`edge:uk1`/…、`all-edges`（= `edge-targets.json` 中所有 `deployable:true` 的 edge，先解析再逐个跑；当前实际只有 us1），或域名。决定 region/instance。 |
| `hours` | 回看小时数。注意 docker logs 仅覆盖容器 `Up` 时长——先 `docker ps` 看 `tokenkey` 启动多久，超出部分日志不存在。 |
| `minutes` | 亚小时窗口；用户说"过去 30 分钟"用 `minutes=30`，直接转 `docker logs --since 32m`（多拉 2min 缓冲让按 `completed_at` 过滤的边界分钟完整）。给了 `minutes` 就忽略 `hours`。 |
| `account` | 账号 id 或 name；`all` 则先列该 platform 的可调度账号再画像。 |
| `bucket` | 默认按分钟；流量大或回看长时用 `5min`。 |

默认：`hours=1`、`account=all`、`path=/v1/messages`、`mode=只读`。planned edge 不查除非 `allow_planned=true`。

> **target=all-edges 的解析**：可调度集 = `deploy/aws/stage0/edge-targets.json` 里 `deployable:true` 的条目（用 `resolve-edge-target.py` 或直接读 JSON）。**不要**对 `deployable:false` 的 planned edge（uk1/sg1/fra1 等）跑画像，除非 `allow_planned=true`。当前矩阵下 `all-edges` 实际只解析出 us1。

## 0) 为什么必须"逐分钟重建"，不能只看 gauge

admin 账号卡片四个数字是**瞬时 gauge**，主要读 Redis，**没有逐分钟历史**：

| 卡片 gauge | Redis 落地 key | 历史保留 |
|---|---|---|
| 🎛 并发 `cur/concurrency` | `concurrency:account:{id}`（zset，活跃 slot） | ❌ 仅当前；`wait:account:{id}` 为等待槽 |
| 💲 窗口费用 `$x/$limit` | `window_cost:account:{id}`（string，**5h 窗边界缓存**，≠简单尾随求和） | ❌ 仅当前；底层 `usage_logs` 有逐请求成本 |
| 👥 会话 `cur/max_sessions` | `session_limit:account:{id}`（zset，按 `session_idle_timeout_minutes` 过期，默认 5、可被 extra 覆盖） | ❌ 仅当前 |
| 🕐 RPM `cur/base_rpm [T]` | `rpm:{id}:{unixMinute}`，**TTL=120s** | ❌ 只留最近 ~2 分钟 |

**结论**：除“当前快照”外，过去 N 小时的逐分钟值只能从 **access log（`http request completed`）+ `sticky.scheduler_entry` + `usage_logs`** 重建。这是本 skill 的核心。

**已踩过的坑**：
1. `grep -c '429'`/`'529'` 是**误报**——会命中 UUID、`body_bytes`、`latency_ms` 里的子串。判真实上游限流/过载要解析 JSON 或匹配 `rate_limit_error`/`overloaded_error`，不要数裸数字。
2. 瞬时 gauge ≤ cap **不代表**历史没触顶（峰值已过、配置事后被改）。务必重建；并确认 cap 在事发时段的取值（如 `max_sessions` 被从 16 改到 30）。
3. account 被判不可用/`no available accounts` 时三个本地 cap（concurrency/max_sessions/base_rpm）都能触发且**不留专门日志**，prod Debug 级 `sticky.layer*` 默认关——只有重建数据能区分。
4. **不要先认定 base_rpm**（本 skill 第一版排障就误判过）。判别口诀：
   - `no available` 那一分钟若 **RPM<base_rpm 且 conc<concurrency**（低负载也 503）→ 几乎一定是 **session 面**：算 `全局活跃会话 vs Σ(max_sessions)`。
   - 现象是「**粘性请求 200、非粘性(新会话/sticky miss)503**」→ 黄区 RPM **或** session 满二选一；用 RPM 序列区分：RPM≥base 选黄区，RPM<base 选 session。
   - 只有某分钟 RPM 真的 ≥base_rpm 才轮到 base_rpm 黄/红区。
5. **重建出的 `activeSess` 是上界，不是触顶证据**（与坑 2 对称）。§3 用 `IDLE_MIN` 尾随窗按 `session_hash` 去重计活跃会话，这个窗通常**比真实 zset 的过期行为更宽**，所以 `activeSess` 常会**高于**当下 `ZCARD session_limit:account:*` 之和，甚至越过 Σ(max_sessions)。**单看 `activeSess>Σmax` 不能判 session 触顶**——必须同时满足「该时段确有 503 / `no available`」且「现象是粘性 200、非粘性失败」。零 503 时 `activeSess` 越线只说明会话维度余量最小、值得盯，**不是**已触顶。核对方式：对照当前 `ZCARD` 之和（live 真值）与该时段真实失败计数。
6. **字段来源混淆 + 数列号陷阱**（2026-05-23 现场踩坑）。accounts 表里 cap 字段一半是**顶层列**（`concurrency / schedulable / rate_limited_at / rate_limit_reset_at / overload_until / temp_unschedulable_until / temp_unschedulable_reason / session_window_*` / `error_message`），一半在 `extra` JSON（`base_rpm / rpm_strategy / rpm_sticky_buffer / max_sessions / session_idle_timeout_minutes / window_cost_limit / stability_tier`）—— 同名字段 `extra->>'concurrency'` 会查到 NULL，必须用 `accounts.concurrency`。**更危险的失败模式**：`psql -t -A -F'|'` 把 20+ 列输出为纯位置 `|` 分隔串、无列头，肉眼数列号几乎必错（曾把第 20 列 `window_cost_limit=1500` 当成第 18 列 `max_sessions`，结论从"session 触顶"翻成"上游 503"）。**硬纪律**：本 skill 所有 cap / 不可调度证据查询**强制用 §1 给出的 `row_to_json` 固化 SQL**，输出形如 `{"id":4,"max_sessions":"100","window_cost_limit":"1500",...}`，字段名跟值粘在一起、物理不可能错列；禁止自由写多列管道 SELECT。下游展示也只能 key=value，禁止"a4: 28/20/100/8/1500/l5" 这种靠列位读的自由文本。
7. **链式失败 / 镜像账号**。prod 上以 `cc-<edge>-oauth`（如 `cc-us1-oauth` / `cc-uk1-oauth`）命名的 anthropic Key 账号，其 credentials 上游就指向对应 edge 域名（`api-<edge>.tokenkey.dev`）。edge 端任何 5xx / `no available accounts` 都会作为 **upstream 503** 透传回 prod；prod 路由层的 `anthropic_upstream_error` 关键词阈值规则会基于这些 transient 503 累计计数，达阈值（默认 3/3）后给该 prod 账号写 `temp_unschedulable_until`（tier-based cooldown，常见 10m），admin UI 即显示「临时不可调度」黄标。**归因纪律**：看到 prod `temp_unschedulable_reason.matched_keyword='anthropic_upstream_error'` 时，**真因在 edge 同时段画像**，不是 prod 本地 cap；必须切到对应 edge 跑一遍 §1+§3 才算定案。把 prod 的 cooldown 当根因 = 漏判 edge 容量问题。

## 1) 先抓 cap 配置 + 不可调度证据 + 当前快照（一段固化脚本，base64 投递）

字段来源（坑 6 详）：

| 用途 | 字段 | 来源 |
|---|---|---|
| 标识 | `id name platform type status` | `accounts` 顶层列 |
| 调度开关 / 即时并发上限 | `schedulable` / `concurrency` | `accounts` 顶层列（**不是** extra） |
| 临时不可调度（admin「黄标」） | `temp_unschedulable_until` / `temp_unschedulable_reason`(jsonb) | `accounts` 顶层列 |
| 上游错误状态 | `rate_limited_at` / `rate_limit_reset_at` / `overload_until` / `error_message` | `accounts` 顶层列 |
| 会话窗口（部分平台用） | `session_window_status` / `session_window_start` / `session_window_end` | `accounts` 顶层列 |
| RPM cap | `base_rpm` / `rpm_strategy` / `rpm_sticky_buffer` | `accounts.extra` (jsonb) |
| 会话 cap | `max_sessions` / `session_idle_timeout_minutes` | `accounts.extra` (jsonb) |
| 费用窗口 cap（≠session 数！） | `window_cost_limit` | `accounts.extra` (jsonb) |
| 稳定性分级 | `stability_tier` | `accounts.extra` (jsonb) |

> **`window_cost_limit` 是 5h 费用窗上限（单位 USD/cents 视配置），不是 max_sessions** —— 这是坑 6 的现场翻车点：值都是几十~几千的整数，列错位时极易混淆。

`temp_unschedulable_reason` jsonb 关键键：`matched_keyword`（`anthropic_upstream_error` / `rate_limit` / …）、`until_unix`、`triggered_at_unix`、`status_code`、`error_message`、`rule_index`、tier-based cooldown 时长（写在 `error_message` 文案里，如 `cooldown=10m0s tier=2`）。

RPM 三区（代码 `Account.CheckRPMSchedulability` / `isAccountSchedulableForRPM`）：
- `buffer = rpm_sticky_buffer`（若设）`else concurrency + max_sessions`，下限 `base_rpm/5`。
- **绿区** `RPM < base_rpm` → 任何请求可调度。
- **黄区** `base_rpm ≤ RPM < base_rpm+buffer` → **仅粘性**（非粘性负载均衡路径会跳过该账号，line `isAccountSchedulableForRPM(acc,false)`）。
- **红区** `RPM ≥ base_rpm+buffer` → 完全不可调度。`rpm_strategy=sticky_exempt` 时无红区。

`schedulable=false`（admin UI「暂停」灰色开关）≠ `temp_unschedulable_until > now()`（admin UI「临时不可调度」黄标）：前者是人手关掉，后者是阈值规则自动打的。归因要分清。

### 1.1 固化脚本（caps + 不可调度证据 + Redis 快照）

**不要现场拼 SELECT。** 复制下面这段，本地写到文件 → base64 → SSM 投递（§3 同款流程）。输出每个账号一行 JSON（cap）+ 一行 KV（Redis），字段名跟值绑死。

```bash
cat <<'BASH' > /tmp/profile_caps.sh
#!/bin/bash
set -uo pipefail
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
RC='docker exec tokenkey-redis redis-cli'
PLATFORM="${PLATFORM:-anthropic}"

echo "=== docker ps tokenkey ==="
docker ps --filter name=tokenkey --format '{{.Names}}\t{{.Status}}\t{{.Image}}'

echo
echo "=== caps + schedulability evidence (one JSON per account; field names embedded) ==="
# row_to_json: 所有字段名嵌在值旁，物理不可能数错列。坑 6 的硬纪律。
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT
    a.id, a.name, a.platform, a.type, a.status, a.schedulable, a.concurrency,
    -- 不可调度证据（顶层列）
    a.rate_limited_at, a.rate_limit_reset_at, a.overload_until,
    a.temp_unschedulable_until, a.temp_unschedulable_reason,
    a.session_window_status, a.session_window_start, a.session_window_end,
    left(COALESCE(a.error_message,''),200) AS error_message,
    -- cap（extra JSON）
    a.extra->>'base_rpm'                       AS base_rpm,
    a.extra->>'rpm_strategy'                   AS rpm_strategy,
    a.extra->>'rpm_sticky_buffer'              AS rpm_sticky_buffer,
    a.extra->>'max_sessions'                   AS max_sessions,
    a.extra->>'session_idle_timeout_minutes'   AS idle_min,
    a.extra->>'window_cost_limit'              AS window_cost_limit,
    a.extra->>'stability_tier'                 AS tier,
    -- 组（如有 account_groups）
    ag.group_id, ag.priority AS group_priority
  FROM accounts a
  LEFT JOIN account_groups ag ON ag.account_id=a.id
  WHERE a.platform='$PLATFORM'
  ORDER BY a.id, ag.group_id NULLS LAST
) t;
" 2>&1

echo
echo "=== Redis snapshot (active accounts, one line per account) ==="
IDS=$($PSQL -c "SELECT string_agg(id::text,' ' ORDER BY id) FROM accounts WHERE platform='$PLATFORM' AND status='active';" 2>/dev/null)
echo "active_ids: $IDS"
for id in $IDS; do
  conc=$($RC ZCARD concurrency:account:$id 2>/dev/null)
  sess=$($RC ZCARD session_limit:account:$id 2>/dev/null)
  wait=$($RC ZCARD wait:account:$id 2>/dev/null)
  wc=$($RC GET    window_cost:account:$id 2>/dev/null)
  rpm=$($RC EVAL "local t=redis.call('TIME'); return redis.call('GET','rpm:'..ARGV[1]..':'..math.floor(tonumber(t[1])/60)) or '0'" 0 $id 2>/dev/null)
  # 字段名贴在值旁；下游解析 = grep+awk 即可，禁止数列号
  echo "redis_snapshot acct=$id conc=$conc sess=$sess wait=$wait wcost=${wc:-} rpm_now=$rpm"
done

echo
echo "=== ops_error_logs last 2h (platform-filtered + schedulability keywords) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD HH24:MI:SS') AS ts_utc,
         severity, error_phase, error_type, status_code, upstream_status_code,
         account_id, model, provider_error_code,
         left(error_message,200) AS error_message
  FROM ops_error_logs
  WHERE created_at >= now()-interval '2 hour'
    AND (platform='$PLATFORM'
         OR error_message ILIKE '%schedulable%'
         OR error_message ILIKE '%no available%'
         OR error_message ILIKE '%cooldown%'
         OR error_message ILIKE '%rate_limit%')
  ORDER BY created_at DESC
  LIMIT 150
) t;
" 2>&1
BASH

# 投递（在调用方本地跑）
B64=$(base64 -i /tmp/profile_caps.sh | tr -d '\n')
CID=$(aws ssm send-command --region "$REGION" --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --parameters "commands=[\"echo $B64 | base64 -d > /tmp/profile_caps.sh && PLATFORM=anthropic bash /tmp/profile_caps.sh\"]" \
  --query Command.CommandId --output text)
# 轮询拉结果（StandardOutputContent；不要在意 StandardErrorContent 里的 AUTH 噪声，见下）
```

> **redis-cli stderr 噪声坑（实测）**：本机即使**不带** `-a`，`redis-cli` 仍可能往 **stderr** 刷一堆 `AUTH failed: ERR AUTH <password> called without any password configured`（容器里设了 `REDISCLI_AUTH`）。这是**无害噪声**——**stdout 的取值是正确的**。SSM `get-command-invocation` 时分别看 `StandardOutputContent`（值）与 `StandardErrorContent`（噪声），不要因 stderr 非空就判失败。

### 1.2 读结果的硬纪律

- **caps 行**：每行一 JSON。要某字段直接 `python3 -c "import sys,json;[print(o[k]) for o in map(json.loads,sys.stdin) for k in ['max_sessions']]"`，或 `jq '.max_sessions'`。**禁止**眼睛数列。
- **redis_snapshot 行**：`acct=X conc=Y sess=Z ...` 形式，每个值前面都有字段名。要 `sess` 就 grep `sess=` 不会错。
- 给用户的报告里，所有 cap 列出必须用「字段名: 值」格式（见 §5）。**禁止** `a4: 10/28/20/100/8/1500/l5` 这种靠列位的自由文本——这是坑 6 的二次失败入口。

## 2) 拉 access log（host 临时文件，只回摘要）

```bash
# SSM 命令里：容器名以 docker ps 实测为准（常见 tokenkey / tokenkey-postgres / tokenkey-redis）
# SINCE：hours 模式用 "${HOURS}h"；minutes 模式用 "$((MINUTES+2))m"（多 2min 缓冲让边界分钟完整）
SINCE="${SINCE:-${HOURS:-1}h}"
docker logs tokenkey --since "$SINCE" 2>&1 | grep 'http request completed' | grep '/v1/messages' > /tmp/acc.txt
docker logs tokenkey --since "$SINCE" 2>&1 | grep 'sticky.scheduler_entry'                        > /tmp/sse.txt
wc -l /tmp/acc.txt /tmp/sse.txt
```

`http request completed` JSON 关键字段：`account_id`、`path`、`status_code`、`latency_ms`、`completed_at`(UTC, `...Z`)。
`sticky.scheduler_entry` JSON：`session_hash`、`sticky_account_id`(>0=粘性命中,0=无绑定走负载均衡)、`sticky_source`(prefetch/…)、`excluded_count`(cooldown 预排除数)。

## 3) 逐分钟重建（远端 Python，输出聚合表）

**投递方式（实测稳定）**：不要把多行 Python 直接塞进 SSM `--parameters` 的 JSON heredoc——引号会地狱级转义失败、`StandardOutputContent` 常空。固定用 **base64 投递**：本地 `base64` 编码脚本 → SSM 命令里 `echo <b64> | base64 -d > /tmp/profile.py` → `python3 /tmp/profile.py`。

**ACCTS / IDLE_MIN 自动发现（省掉 §1 → §3 的二次往返）**：脚本不再手填账号 id 和 idle，而是先在远端查一次 DB 派生，导成环境变量给 Python：

```bash
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
# 可调度账号 → ACCTS；session idle（取最大值做尾随窗，宁宽勿窄）→ IDLE_MIN
export ACCTS=$($PSQL -c "SELECT string_agg(id::text,',' ORDER BY id) FROM accounts WHERE platform='anthropic' AND schedulable AND status='active';")
export IDLE_MIN=$($PSQL -c "SELECT COALESCE(MAX(NULLIF(extra->>'session_idle_timeout_minutes','')::int),5) FROM accounts WHERE platform='anthropic' AND schedulable AND status='active';")
# 指定 account=<id> 时：export ACCTS=<id> 覆盖即可
```

Python（读 env，零手填）：

```python
import os, json, re, datetime as dt, collections
ACCTS=[int(x) for x in os.environ.get('ACCTS','').split(',') if x.strip()]
IDLE_MIN=int(os.environ.get('IDLE_MIN','5'))
FMT=os.environ.get('FMT','%H:%M')   # minute；5min 改自定义
def parse(fn):
    out=[]
    try:
        for ln in open(fn):
            m=re.search(r'\{.*\}$',ln)
            if not m: continue
            try: o=json.loads(m.group(0))
            except: continue
            out.append((ln[:19],o))
    except FileNotFoundError: pass
    return out

# --- access log: RPM(按开始分钟) + 峰值并发 + status（status 跟 interval 一起存，供逐分钟 200 用）---
iv={a:[] for a in ACCTS}; rpm={a:collections.Counter() for a in ACCTS}; st={a:collections.Counter() for a in ACCTS}
for _,o in parse('/tmp/acc.txt'):
    if o.get('path')!='/v1/messages': continue
    a=o.get('account_id'); lat=o.get('latency_ms'); ca=o.get('completed_at'); sc=o.get('status_code')
    if a not in ACCTS or not ca or not isinstance(lat,(int,float)): continue
    end=dt.datetime.fromisoformat(ca.replace('Z','+00:00')); start=end-dt.timedelta(milliseconds=lat)
    iv[a].append((start,end,sc)); rpm[a][start.strftime(FMT)]+=1; st[a][sc]+=1
def peak_conc(intervals):
    res=collections.Counter()
    if not intervals: return res
    lo=min(s for s,_,_ in intervals).replace(second=0,microsecond=0); hi=max(e for _,e,_ in intervals); t=lo
    while t<=hi:
        c=sum(1 for s,e,_ in intervals if s<=t<e)
        res[t.strftime(FMT)]=max(res[t.strftime(FMT)],c); t+=dt.timedelta(seconds=5)
    return res
pc={a:peak_conc(iv[a]) for a in ACCTS}
# 逐分钟 200 数（按 start 落分钟；≠整段总数，整段总数见末尾 statuses=）
ok={a:collections.Counter() for a in ACCTS}
for a in ACCTS:
    for s,_,sc in iv[a]:
        if sc==200: ok[a][s.strftime(FMT)]+=1

# --- sticky vs non-sticky RPM split（按 sticky_account_id 落到分钟）---
srpm={a:collections.Counter() for a in ACCTS}; nrpm=collections.Counter()  # 非粘性 sticky_account_id=0，账号未知，单列
for ts,o in parse('/tmp/sse.txt'):
    try: t=dt.datetime.strptime(ts,'%Y-%m-%dT%H:%M:%S')
    except: continue
    k=t.strftime(FMT); aid=o.get('sticky_account_id')
    if aid in ACCTS: srpm[aid][k]+=1
    elif aid in (0,None): nrpm[k]+=1

# --- 活跃会话（global，trailing IDLE 窗）。注意：这是上界，可能高过 live ZCARD 之和，单独不构成 session 触顶证据（见 §0 坑 5）---
rows=[]
for ts,o in parse('/tmp/sse.txt'):
    sh=o.get('session_hash')
    try: t=dt.datetime.strptime(ts,'%Y-%m-%dT%H:%M:%S')
    except: continue
    if sh: rows.append((t,sh))
sess=collections.Counter()
if rows:
    lo=min(r[0] for r in rows).replace(second=0); hi=max(r[0] for r in rows).replace(second=0); W=dt.timedelta(minutes=IDLE_MIN); t=lo
    while t<=hi:
        seen=set(sh for rt,sh in rows if t-W < rt <= t+dt.timedelta(seconds=59))
        sess[t.strftime(FMT)]=len(seen); t+=dt.timedelta(minutes=1)

mins=sorted(set().union(*[set(rpm[a]) for a in ACCTS],*[set(pc[a]) for a in ACCTS],set(sess),set(nrpm)))
print('min  | '+' '.join('a%d:rpm/sRpm/conc/ok200'%a for a in ACCTS)+' | nonStkRpm actSess(g,上界)')
for mn in mins:
    seg=' '.join('%2d/%2d/%2d/%2d'%(rpm[a][mn],srpm[a][mn],pc[a][mn],ok[a][mn]) for a in ACCTS)  # ok200=该分钟 200 数（逐分钟，非死值）
    print('%s | %s | %3d  %3d'%(mn,seg,nrpm[mn],sess[mn]))
for a in ACCTS:
    print('acct%d totals reqs=%d rpm_max=%d conc_max=%d statuses=%s'%(a,len(iv[a]),max(rpm[a].values() or [0]),max(pc[a].values() or [0]),dict(st[a])))
```

> 逐分钟表第 4 个分量 `ok200` 是**该分钟**完成的 200 数（按 start 落分钟）；某分钟 `rpm > ok200` 即说明该分钟有非 200（失败/限流）。整段 200/4xx/5xx 总数看末尾每账号的 `statuses=` 字典。**不要**用整段总数当逐分钟值（旧模板曾把 `st[a].get(200,0)` 印在每行，是死值 bug）。

成本逐分钟（DB，独立 SQL；`window_cost` gauge 是 5h 窗缓存，逐分钟用 `usage_logs`）：

```sql
SELECT to_char(date_trunc('minute',created_at),'HH24:MI') min_utc, account_id,
       count(*) reqs, round(sum(total_cost),4) cost
FROM usage_logs
WHERE account_id = ANY($IDS) AND created_at >= now()-interval '$HOURS hours'
GROUP BY 1,2 ORDER BY 1,2;
-- 5h 窗累计校准卡片 $ gauge：
SELECT account_id, round(sum(total_cost),2) cost_5h FROM usage_logs
WHERE account_id = ANY($IDS) AND created_at >= now()-interval '5 hours' GROUP BY 1;
```

## 4) 解读规则（哪个参数触顶）

| 观察 | 判定 |
|---|---|
| 某分钟 `rpm ≥ base_rpm` 且非粘性请求失败/`no available` | **base_rpm 黄/红区**：非粘性被 RPM 闸挤出。 |
| `peak_conc ≈ concurrency` 且新请求 429/排队超时 | **concurrency 触顶**（`Concurrency limit exceeded` 或 `umq`/wait 超时）。 |
| `global activeSess > Σ(max_sessions)` **且该时段确有 503/`no available`** 且**粘性 200、非粘性失败** | **max_sessions 触顶**：新会话被 `checkAndRegisterSession` 拒（`ErrNoAvailableAccounts`，gateway_service.go ~line 2121），已绑定会话 `ZSCORE` 命中放行。 |
| `activeSess > Σ(max_sessions)` **但零 503**（全程 200） | **未触顶**：`activeSess` 是 IDLE 窗上界、高于 live `ZCARD` 之和（见 §0 坑 5）。结论=会话维度余量最小、值得盯，**不是**触顶；核对当前 `ZCARD` 之和与真实失败计数。 |
| RPM<base、conc<max、sess 未饱和，却仍 503 | 查上游：解析 JSON 找 `rate_limit_error`/`overloaded_error`/`cooldown`/`rate_limit_reset_at`，**别数裸 429/529**。 |
| `nonStickyRpm` 高、`activeSess` 接近 Σmax | 单 CLI 派生大量短会话 → 会话面先到顶（典型：edge 仅 2 账号时）。 |
| prod 账号 `temp_unschedulable_until > now()` 且 `temp_unschedulable_reason.matched_keyword='anthropic_upstream_error'` | **链式失败**：prod 把 edge 透传回来的 503 累计到本地阈值规则，自动 cooldown。**这不是根因**——切到对应 edge 跑 §1+§3 找 edge 实因（max_sessions / concurrency / 真上游 503）。归因责任在 edge 同时段画像。 |

判 session 饱和的关键不等式：**全局活跃会话 > Σ(max_sessions over 可调度账号)** ⇒ 必有新会话落空。务必用**事发时段**的 `max_sessions`（可能被事后调过）。

### 4.1 链式失败 / 镜像账号识别

prod 上 anthropic Key 账号若命名形如 `cc-<edge>-oauth`（如 `cc-us1-oauth` → `api-us1.tokenkey.dev`），归因路径必须是双跳：

```
[edge 实因: max_sessions / concurrency / base_rpm / 真上游 503]
       │
       ▼ 503 透传 (upstream_status_code=503, body="no available accounts" 等)
[prod 路由层 anthropic_upstream_error 关键词阈值规则: 累计 N/N]
       │
       ▼ 写 accounts.temp_unschedulable_until = now()+cooldown(tier-based, 常见 10m)
[admin UI: 临时不可调度黄标]
```

确认是否镜像账号：`SELECT credentials->>'base_url' FROM accounts WHERE id=<prod_acct_id>;`（或 `credentials->>'endpoint'`，依字段名而定）。base_url 指向 `api-<edge>.tokenkey.dev/*` 即镜像。

操作上：先在 prod 跑一次 §1 拿 `temp_unschedulable_reason`，从 `triggered_at_unix` 反推 edge 上的事发分钟（同一秒精度），再到 edge 跑 §1+§3，对照那几分钟的 `actSess / sRPM / nonStk / ZCARD-now / max_sessions / concurrency` 才能定真因。

## 5) 输出模板

**强制 key=value / JSON**：每个数字前面必须挨着字段名。**禁止**列号风格的自由文本（`a4: 10/28/20/100/8/1500/l5` 这种），否则坑 6 会复发。

```text
target=<...>  time_window_utc=<..>..<..>  time_window_local=<..>  bucket=minute

accounts:                                # 每行一账号，字段名: 值，禁止单行多值无名
- id=4 name=am-us-ec2-5-1-b status=active schedulable=true
    concurrency=10  base_rpm=28  rpm_strategy=tiered  rpm_sticky_buffer=20
    max_sessions=100  idle_min=8  window_cost_limit=1500  tier=l5
    session_window_status=allowed
    session_window_start=2026-05-22T21:00:00Z  session_window_end=2026-05-23T02:00:00Z
    temp_unschedulable_until=- temp_unschedulable_reason.matched_keyword=-

caps_snapshot(redis now):
- acct=4 conc=0 sess=12 wait=0 wcost=- rpm_now=0

peaks(过去Nh):
- acct=4 rpm_max=19@01:13 conc_max=- activeSess_max(global)=108@01:10 reqs=N 5h_cost=$N
limit_touched: <base_rpm | concurrency | max_sessions | upstream | chained-from:<edge>>  置信度 high|med|low
evidence:
- 01:09 UTC sRPM=9 nonStk=6 totalReq=15(<base=28) actSess=102(>max=100) 503=3 → max_sessions 触顶
- 01:14 UTC sRPM=13 nonStk=14 totalReq=27(≈base=28) actSess=88(<max=100) 503=3 → base_rpm 黄区 / conc 临界

per_minute_table: <见 §3 输出；超长则写 $CLAUDE_JOB_DIR 文件，仅回摘要+路径>
```

报告里出现的每个数字必须能在 SSM stdout 里以 `字段名=值` 或 JSON `"字段名":值` 形式 grep 到原文。如果 grep 不到，**就是从列号读出来的——回去用 §1.1 的固化脚本重抓**。

证据不足（如 docker logs 未覆盖整段、账号无流量）就说明并缩短 `hours` 或换 target，不要外推。镜像账号（§4.1）必须给出双跳归因，单跳报告视为未完成。

## 6) 交接

需要改 cap（`base_rpm`/`max_sessions`/`concurrency`/priority）→ 不在本 skill 内写；输出 plan 后交 `tokenkey-anthropic-oauth-config`（tier baseline / account 字段）或 admin UI（`group.rpm_limit` 独立设置）。冗余不足导致落空 503 的同类问题参见运维记忆与 `tokenkey-online-log-troubleshooting`。
