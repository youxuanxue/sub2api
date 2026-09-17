## 1) 先抓 cap 配置 + 不可调度证据 + 当前快照（调用 probe-caps.sh）

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
| 上游窗口利用率（调度 soft gate） | `session_window_utilization` / `passive_usage_7d_utilization` | `accounts.extra` (jsonb，被动采样) |
| 稳定性分级 | `stability_tier` | `accounts.extra` (jsonb) |

> **上游 5h/7d 利用率 ≠ session 数 ≠ RPM** —— 列错位时极易混淆；调度 gate 为 global 0.98/0.02，不再读 tier 美元 cap。

`temp_unschedulable_reason` jsonb 关键键：`matched_keyword`（`anthropic_upstream_error` / `rate_limit` / …）、`until_unix`、`triggered_at_unix`、`status_code`、`error_message`、`rule_index`、tier-based cooldown 时长（写在 `error_message` 文案里，如 `cooldown=10m0s tier=2`）。

RPM 三区（代码 `Account.CheckRPMSchedulability` / `isAccountSchedulableForRPM`）：
- `buffer = rpm_sticky_buffer`（若设）`else concurrency + max_sessions`，下限 `base_rpm/5`。
- **绿区** `RPM < base_rpm` → 任何请求可调度。
- **黄区** `base_rpm ≤ RPM < base_rpm+buffer` → **仅粘性**（非粘性负载均衡路径会跳过该账号，line `isAccountSchedulableForRPM(acc,false)`）。
- **红区** `RPM ≥ base_rpm+buffer` → 完全不可调度。`rpm_strategy=sticky_exempt` 时无红区。

`schedulable=false`（admin UI「暂停」灰色开关）≠ `temp_unschedulable_until > now()`（admin UI「临时不可调度」黄标）：前者是人手关掉，后者是阈值规则自动打的。归因要分清。

### 1.1 调用 probe-caps.sh（机械化抓取，零 prose SQL）

固化在 `ops/observability/probe-caps.sh`（dev-rules 「机械化优于 prompt 推断」基线：本可机械化的步骤由脚本承载，prompt 只描述调用接口与真实判断）。该脚本在远端运行 psql + redis-cli，输出三段：

| 段 | 形态 | 解析方式 |
|---|---|---|
| `docker ps` 块 | 文本表 | 肉眼或 grep 容器名 |
| caps + 不可调度证据 | **每行一 JSON**（`row_to_json`） | `jq '.max_sessions'` / `json.loads` 按字段名取值 |
| Redis snapshot | `redis_snapshot acct=N conc=N sess=N wait=N wcost=… rpm_now=N` | grep `字段名=` |
| ops_error_logs 近 2h | 每行一 JSON | 同上 |

字段名嵌在值旁，**物理不可能数错列**——这就是坑 6 的硬约束载体。

调用（远端在 SSM 里跑，全部由 `run-probe.sh` 统一投递）：

```bash
# prod / edge 都走同一个 wrapper；它负责 region/instance 解析 + base64 投递 + send + poll
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-caps.sh \
  --env PLATFORM=anthropic \
  --env ERR_HOURS=2

# deployable Lightsail edge 同款
bash ops/observability/run-probe.sh \
  --target edge:us1 \
  --script ops/observability/probe-caps.sh \
  --env PLATFORM=anthropic
```

环境变量（脚本顶部 contract）：`PLATFORM`（默认 `anthropic`）、`ERR_HOURS`（默认 2）、`ERR_LIMIT`（默认 150）。新增字段只在脚本里改一次——不再回头同步 SKILL 文本。**禁止**手写 base64 投递 / send-command 调用：所有漂移点都收敛在 `run-probe.sh` 内。

> **redis-cli stderr 噪声坑（实测）**：容器里设了 `REDISCLI_AUTH`，即使**不带** `-a`，`redis-cli` 仍可能往 **stderr** 刷 `AUTH failed: ERR AUTH <password> called without any password configured`。这是**无害噪声**——`StandardOutputContent` 是正确的；不要因 `StandardErrorContent` 非空就判失败。

### 1.2 读结果的硬纪律（坑 6 的执行面）

- **caps 行**：每行一 JSON。要某字段直接 `jq '.max_sessions'` 或 `python3 -c "import sys,json;[print(json.loads(l).get('max_sessions')) for l in sys.stdin if l.strip()]"`。**禁止**眼睛数列号。
- **redis_snapshot 行**：grep `sess=` 不会错位。
- 给用户的报告里，所有 cap 列出必须用「字段名: 值」格式（见 §5）。**禁止** `a4: 10/28/20/100/8/1500/l5` 这种靠列位的自由文本——这是坑 6 的二次失败入口。
