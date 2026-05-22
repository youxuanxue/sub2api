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
/tokenkey-online-traffic-profile target=<prod|edge:<id>|domain> [hours=<N，默认1>] [account=<id|name|all，默认all>] [model=<name>] [path=/v1/messages] [bucket=minute|5min] [allow_planned=false]
```

| 参数 | 语义 |
|---|---|
| `target` | `prod`、`edge:us1`/`edge:uk1`/…，或域名。决定 region/instance。 |
| `hours` | 回看小时数。注意 docker logs 仅覆盖容器 `Up` 时长——先 `docker ps` 看 `tokenkey` 启动多久，超出部分日志不存在。 |
| `account` | 账号 id 或 name；`all` 则先列该 platform 的可调度账号再画像。 |
| `bucket` | 默认按分钟；流量大或回看长时用 `5min`。 |

默认：`hours=1`、`account=all`、`path=/v1/messages`、`mode=只读`。planned edge 不查除非 `allow_planned=true`。

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

## 1) 先抓 cap 配置 + 当前快照（一次 SSM）

```sql
-- accounts cap 配置（cap 在事发时段的真实值）
SELECT id,name,type,status,schedulable,concurrency,
       extra->>'base_rpm'                    AS base_rpm,
       extra->>'rpm_strategy'                AS rpm_strategy,      -- tiered | sticky_exempt
       extra->>'rpm_sticky_buffer'           AS rpm_sticky_buffer, -- 黄区宽度 override
       extra->>'max_sessions'                AS max_sessions,
       extra->>'session_idle_timeout_minutes' AS idle_min,         -- 默认 5
       extra->>'window_cost_limit'           AS window_cost_limit,
       extra->>'stability_tier'              AS tier
FROM accounts
WHERE platform='anthropic' AND ($ACCOUNT='all' OR id=$ID OR name='$NAME')
ORDER BY id;
```

RPM 三区（代码 `Account.CheckRPMSchedulability` / `isAccountSchedulableForRPM`）：
- `buffer = rpm_sticky_buffer`（若设）`else concurrency + max_sessions`，下限 `base_rpm/5`。
- **绿区** `RPM < base_rpm` → 任何请求可调度。
- **黄区** `base_rpm ≤ RPM < base_rpm+buffer` → **仅粘性**（非粘性负载均衡路径会跳过该账号，line `isAccountSchedulableForRPM(acc,false)`）。
- **红区** `RPM ≥ base_rpm+buffer` → 完全不可调度。`rpm_strategy=sticky_exempt` 时无红区。

当前 Redis 快照（确认这 4 个数字"确有落地"，并校准）。Redis **无密码**：不要带 `-a`。

> **stderr 噪声坑（实测）**：本机即使**不带** `-a`，`redis-cli` 仍可能往 **stderr** 刷一堆 `AUTH failed: ERR AUTH <password> called without any password configured`（容器里设了 `REDISCLI_AUTH`）。这是**无害噪声**——**stdout 的取值是正确的**。读结果只看 stdout，别被 stderr 吓到；SSM `get-command-invocation` 时分别看 `StandardOutputContent`（值）与 `StandardErrorContent`（噪声），不要因 stderr 非空就判失败。

```bash
RC='docker exec tokenkey-redis redis-cli'
for id in $IDS; do
  echo "acct $id  conc=$($RC ZCARD concurrency:account:$id)  sess=$($RC ZCARD session_limit:account:$id)  wcost=$($RC GET window_cost:account:$id)"
done
# rpm 当前分钟（unixMinute=服务端秒/60；TTL=120s，过去分钟已过期）：
$RC EVAL "local t=redis.call('TIME'); return redis.call('GET','rpm:'..ARGV[1]..':'..math.floor(tonumber(t[1])/60)) or '0'" 0 $ID
```

## 2) 拉 access log（host 临时文件，只回摘要）

```bash
# SSM 命令里：容器名以 docker ps 实测为准（常见 tokenkey / tokenkey-postgres / tokenkey-redis）
docker logs tokenkey --since ${HOURS}h 2>&1 | grep 'http request completed' | grep '/v1/messages' > /tmp/acc.txt
docker logs tokenkey --since ${HOURS}h 2>&1 | grep 'sticky.scheduler_entry'                       > /tmp/sse.txt
wc -l /tmp/acc.txt /tmp/sse.txt
```

`http request completed` JSON 关键字段：`account_id`、`path`、`status_code`、`latency_ms`、`completed_at`(UTC, `...Z`)。
`sticky.scheduler_entry` JSON：`session_hash`、`sticky_account_id`(>0=粘性命中,0=无绑定走负载均衡)、`sticky_source`(prefetch/…)、`excluded_count`(cooldown 预排除数)。

## 3) 逐分钟重建（远端 Python，输出聚合表）

把下面整段放进 SSM 命令的 heredoc 远端执行。`ACCTS` 改成目标账号 id；`IDLE_MIN` 用 §1 查到的 `session_idle_timeout_minutes`。

```python
import json, re, datetime as dt, collections
ACCTS=[1,4]; IDLE_MIN=8           # ← 按 §1 实测填写
START_BUCKET='%H:%M'              # 'minute'; 5min 改成自定义
def parse(fn):
    out=[]
    for ln in open(fn):
        m=re.search(r'\{.*\}$',ln)
        if not m: continue
        try: o=json.loads(m.group(0))
        except: continue
        out.append((ln[:19],o))
    return out

# --- access log: RPM(按开始分钟) + 峰值并发 + status ---
iv={a:[] for a in ACCTS}; rpm={a:collections.Counter() for a in ACCTS}; st={a:collections.Counter() for a in ACCTS}
for _,o in parse('/tmp/acc.txt'):
    if o.get('path')!='/v1/messages': continue
    a=o.get('account_id'); lat=o.get('latency_ms'); ca=o.get('completed_at')
    if a not in ACCTS or not ca or not isinstance(lat,(int,float)): continue
    end=dt.datetime.fromisoformat(ca.replace('Z','+00:00')); start=end-dt.timedelta(milliseconds=lat)
    iv[a].append((start,end)); rpm[a][start.strftime(START_BUCKET)]+=1; st[a][o.get('status_code')]+=1
def peak_conc(intervals):
    res=collections.Counter()
    if not intervals: return res
    lo=min(s for s,_ in intervals).replace(second=0,microsecond=0); hi=max(e for _,e in intervals)
    t=lo
    while t<=hi:
        c=sum(1 for s,e in intervals if s<=t<e)
        k=t.strftime(START_BUCKET); res[k]=max(res[k],c); t+=dt.timedelta(seconds=5)
    return res
pc={a:peak_conc(iv[a]) for a in ACCTS}

# --- sticky vs non-sticky RPM split（按 sticky_account_id 落到分钟）---
srpm={a:collections.Counter() for a in ACCTS}; nrpm=collections.Counter()  # 非粘性 sticky_account_id=0，账号未知，单列
for ts,o in parse('/tmp/sse.txt'):
    try: t=dt.datetime.strptime(ts,'%Y-%m-%dT%H:%M:%S')
    except: continue
    k=t.strftime(START_BUCKET); aid=o.get('sticky_account_id')
    if aid in ACCTS: srpm[aid][k]+=1
    elif aid in (0,None): nrpm[k]+=1

# --- 活跃会话（global，trailing IDLE 窗）---
rows=[]
for ts,o in parse('/tmp/sse.txt'):
    sh=o.get('session_hash')
    try: t=dt.datetime.strptime(ts,'%Y-%m-%dT%H:%M:%S')
    except: continue
    if sh: rows.append((t,sh,o.get('sticky_account_id')))
sess=collections.Counter()
if rows:
    lo=min(r[0] for r in rows).replace(second=0); hi=max(r[0] for r in rows).replace(second=0); W=dt.timedelta(minutes=IDLE_MIN); t=lo
    while t<=hi:
        seen=set(sh for rt,sh,_ in rows if t-W < rt <= t+dt.timedelta(seconds=59))
        sess[t.strftime(START_BUCKET)]=len(seen); t+=dt.timedelta(minutes=1)

mins=sorted(set().union(*[set(rpm[a]) for a in ACCTS],*[set(pc[a]) for a in ACCTS],set(sess)))
hdr='min  | '+' '.join('a%d:rpm/sRpm/conc/st200'%a for a in ACCTS)+' | nonStickyRpm activeSess(global)'
print(hdr)
for mn in mins:
    seg=' '.join('%2d/%2d/%2d/%-3d'%(rpm[a][mn],srpm[a][mn],pc[a][mn],st[a].get(200,0)) for a in ACCTS)
    print('%s | %s | %3d  %3d'%(mn,seg,nrpm[mn],sess[mn]))
for a in ACCTS:
    print('acct%d totals reqs=%d rpm_max=%d conc_max=%d statuses=%s'%(a,len(iv[a]),max(rpm[a].values() or [0]),max(pc[a].values() or [0]),dict(st[a])))
```

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
| `global activeSess > Σ(max_sessions)` 且**粘性 200、非粘性 503** | **max_sessions 触顶**：新会话被 `checkAndRegisterSession` 拒（`ErrNoAvailableAccounts`，gateway_service.go ~line 2121），已绑定会话 `ZSCORE` 命中放行。 |
| RPM<base、conc<max、sess 未饱和，却仍 503 | 查上游：解析 JSON 找 `rate_limit_error`/`overloaded_error`/`cooldown`/`rate_limit_reset_at`，**别数裸 429/529**。 |
| `nonStickyRpm` 高、`activeSess` 接近 Σmax | 单 CLI 派生大量短会话 → 会话面先到顶（典型：edge 仅 2 账号时）。 |

判 session 饱和的关键不等式：**全局活跃会话 > Σ(max_sessions over 可调度账号)** ⇒ 必有新会话落空。务必用**事发时段**的 `max_sessions`（可能被事后调过）。

## 5) 输出模板

```text
target=<...>  time_window_utc=<..>..<..>  time_window_local=<..>  bucket=minute
accounts: <id(name): base_rpm=.. buffer=.. max_sessions=.. concurrency=.. idle=..m>
caps_snapshot(redis now): <acct: conc/sess/wcost>

peaks(过去Nh):
- acct<id>: rpm_max=.. (<min>)  conc_max=..  activeSess_max(global)=..  reqs=..  5h_cost=$..
limit_touched: <base_rpm | concurrency | max_sessions | none/upstream>  置信度 high|med|low
evidence: <触顶分钟与现象的对应；区分 final status vs upstream events>

per_minute_table: <见 §3 输出；超长则写 $CLAUDE_JOB_DIR 文件，仅回摘要+路径>
```

证据不足（如 docker logs 未覆盖整段、账号无流量）就说明并缩短 `hours` 或换 target，不要外推。

## 6) 交接

需要改 cap（`base_rpm`/`max_sessions`/`concurrency`/priority）→ 不在本 skill 内写；输出 plan 后交 `tokenkey-anthropic-oauth-config`（tier baseline / account 字段）或 admin UI（`group.rpm_limit` 独立设置）。冗余不足导致落空 503 的同类问题参见运维记忆与 `tokenkey-online-log-troubleshooting`。
