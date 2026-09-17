## 3) 逐分钟重建（调用 profile-traffic.py）

固化在 `ops/observability/profile-traffic.py`。读 `/tmp/acc.txt` + `/tmp/sse.txt`，输出每分钟一行：

```
min(UTC) | aN  :rpm/sR/conc/ok/bad … | nonStk actSess(g)
```

末尾每账号一行 `acctN totals reqs=… rpm_max=… conc_max=… statuses={…}`。

**投递方式**：与 §1.1 同款——通过 `ops/observability/run-probe.sh` 包了 base64 投递 + 远端拉脚本 + env 注入；**禁止**手写完整的 base64 / send-command 链。

`ACCTS` / `IDLE_MIN` 在远端按 psql 派生（不让 operator 手填）：

```bash
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
ACCTS=$($PSQL -c "SELECT string_agg(id::text, ',' ORDER BY id) FROM accounts WHERE platform='anthropic' AND schedulable AND status='active';")
IDLE_MIN=$($PSQL -c "SELECT COALESCE(MAX(NULLIF(extra->>'session_idle_timeout_minutes','')::int), 5) FROM accounts WHERE platform='anthropic' AND schedulable AND status='active';")
ACCTS=$ACCTS IDLE_MIN=$IDLE_MIN python3 /tmp/profile-traffic.py
```

上面这段派生 + 调用是一份**远端**薄壳，由 §1.1 提到的 `run-probe.sh` 投递（脚本作为本机文件传输到远端 `/tmp/`）。如果以后这段薄壳被频繁复用，**应该**抽出为一个独立的 driver 脚本放进 `ops/observability/` 下，届时连同它一起加入 §1.1 的工具表；在那之前不要把这段派生 prose 当作另一份 contract。

env 契约（脚本 docstring 是 ground truth，这里只列要点）：

| env | 用途 | 默认 |
|---|---|---|
| `ACCTS` | 逗号分隔账号 id（必填） | — |
| `IDLE_MIN` | session 活跃尾随窗（分钟），= MAX(account.idle_min) | 5 |
| `PATH_KEY` | 路径过滤（与 §2 必须一致） | `/v1/messages` |
| `FMT` | 输出时间列的 `strftime` 格式（**只换显示格式，不换桶粒度**——桶始终是分钟） | `%H:%M` |

> 逐分钟表 `ok` / `bad` 是**该分钟**完成的 200 / 非 200 数（按 request start 落分钟）；某分钟 `rpm > ok` 即该分钟有失败。整段 status 分布看末尾 `statuses={…}` 字典。**不要**把整段总数当逐分钟值（旧模板曾踩此坑）。

用量成本逐分钟（DB，独立 SQL；与调度 gate 无关，仍来自 `usage_logs`）：

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
