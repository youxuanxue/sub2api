---
name: tokenkey-online-traffic-profile
description: >-
  Read-only TokenKey prod/edge traffic profiling. Use to reconstruct per-minute RPM, sticky/load-balance split, sessions, concurrency, cap pressure, admin account-card gauges, or no-available-accounts throttling evidence.
---

# TokenKey：线上请求流量画像（逐分钟 RPM / sticky / session / concurrency）

把"过去 N 小时某账号/edge 的流量与限额命中情况"固定成稳定的**只读**重建流程。专治：流量趋势、admin 账号卡片四个 gauge 的核对、`no available accounts` / 节流的归因。

权威纪律以仓库根 `CLAUDE.md` 为准。本 skill **只读**：只跑 `docker logs` / `psql SELECT` / `redis-cli` 读命令 / `aws ... describe|get|send-command(只读脚本)`。任何写配置、改 `max_sessions`/`base_rpm`、重启、部署都必须另行显式确认，并交给写入面 skill（`tokenkey-anthropic-oauth-config` 等）。

环境识别（prod/edge 实例解析、容器名、SSM 执行、UTC+本地双写、小输出优先）与 `tokenkey-online-log-troubleshooting` 完全一致——本 skill 复用它的 §1/§2/§3，不重复；下面只写流量画像特有的部分。

## 确定性基线（机械化 vs 真判断）

采样由 `ops/observability/run-probe.sh` 投递 `probe-caps.sh`、`probe-traffic-logs.sh`，重建用 `profile-traffic.py`；选择其它窗口/用户/fleet probe 时才查目录。模型负责证据归因。 见 [操作细则](references/tools.md)。

## 调用参数

```text
/tokenkey-online-traffic-profile target=<prod|edge:<id>|all-edges|domain> [hours=<N，默认1>] [minutes=<M>] [account=<id|name|all，默认all>] [model=<name>] [path=/v1/messages] [allow_planned=false]
```

| 参数 | 语义 |
|---|---|
| `target` | `prod`、`edge:us1`/`edge:uk1`/…、`all-edges`（= Lightsail 矩阵中所有 `deployable:true` 的 edge），或域名。决定 region/instance（EC2 `i-*` 或 Lightsail `mi-*`）。 |
| `hours` | 回看小时数。注意 docker logs 仅覆盖容器 `Up` 时长——先 `docker ps` 看 `tokenkey` 启动多久，超出部分日志不存在。 |
| `minutes` | 亚小时窗口；用户说"过去 30 分钟"用 `minutes=30`，直接转 `docker logs --since 32m`（多拉 2min 缓冲让按 `completed_at` 过滤的边界分钟完整）。给了 `minutes` 就忽略 `hours`。 |
| `account` | 账号 id 或 name；`all` 则先列该 platform 的可调度账号再画像。 |

默认：`hours=1`、`account=all`、`path=/v1/messages`、`mode=只读`、桶=分钟。planned edge 不查除非 `allow_planned=true`。当前桶只支持分钟；需要 5-min 等更粗桶就在分钟输出上做 rollup，不要靠 `FMT` 偷桥（`strftime` 无法表达 5-min 桶）。

> **target=all-edges 的解析**：可调度集由 `python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable` 从 Lightsail 矩阵确定。不要对 `deployable:false` 的 planned edge 跑画像。

> **Lightsail edge SSM**：`uk1` 等用 Hybrid managed instance（tag `EdgeId` + `Platform=lightsail`），**不要**查 `tokenkey-edge-uk1-stage0` CFN `InstanceId`。

## 0) 为什么必须"逐分钟重建"，不能只看 gauge

历史逐分钟值用 access/sticky/usage logs 重建，admin gauge 没有历史。禁止 grep 数字计错、把 base_rpm 当全部硬 cap、按 SQL 列号读取、把 prod 中间 upstream 错误当 edge 死活；首次解释 cap、会话/粘性、镜像链路或窗口缺失时读陷阱细则。 见 [操作细则](references/interpretation-traps.md)。

## 1) 先抓 cap 配置 + 不可调度证据 + 当前快照（调用 probe-caps.sh）

采样前读本节细则，用 `probe-caps.sh` 采当前 cap/可调度态，再采历史日志；SSM/SQL 统一现有脚本，字段名 JSON，不临时多列 SELECT。 见 [操作细则](references/capture.md)。

## 2) 拉 access log（调用 probe-traffic-logs.sh）

固化在 `ops/observability/probe-traffic-logs.sh`。它做的事：

```
docker logs $CONTAINER --since $SINCE | grep -F 'http request completed' | grep -F "$PATH_KEY" > /tmp/acc.txt
docker logs $CONTAINER --since $SINCE | grep -F 'sticky.scheduler_entry'                        > /tmp/sse.txt
```

并打印一行 `probe_traffic_logs container=… since=… path_key=… acc_lines=N sse_lines=N`；两边都为 0 时往 stderr 报一行 WARN（提示 log-format drift / 容器名错 / SINCE 超出 docker 保留窗）。

环境：`SINCE`（默认 `1h`，minutes 模式传 `$((MIN+2))m`）、`PATH_KEY`（默认 `/v1/messages`）、`CONTAINER`（默认 `tokenkey`）。

`http request completed` JSON 关键字段：`account_id`、`path`、`status_code`、`latency_ms`、`completed_at`(UTC, `...Z`)。
`sticky.scheduler_entry` JSON：`session_hash`、`sticky_account_id`(>0=粘性命中,0=无绑定走负载均衡)、`sticky_source`(prefetch/…)、`excluded_count`(cooldown 预排除数)。

## 3) 逐分钟重建（调用 profile-traffic.py）

重建时读本节的 `profile-traffic.py` 参数；5h cost-window 校准只在相关诊断中读取 SQL 段。 见 [操作细则](references/reconstruction.md)。

## 4) 解读规则（哪个参数触顶）

得出 cap/镜像链路归因前读取：必须匹配同窗口的 sticky、503/429 与 edge 自身证据。不要只凭单个 gauge 下结论。 见 [操作细则](references/attribution.md)。

## 5) 输出模板

**强制 key=value / JSON**：每个数字前面必须挨着字段名。**禁止**列号风格的自由文本（`a4: 10/28/20/100/8/1500/l5` 这种），否则坑 6 会复发。

```text
target=<...>  time_window_utc=<..>..<..>  time_window_local=<..>

accounts:                                # 每行一账号，字段名: 值，禁止单行多值无名
- id=4 name=am-us-ec2-5-1-b status=active schedulable=true
    concurrency=10  base_rpm=28  rpm_strategy=tiered  rpm_sticky_buffer=20
    max_sessions=100  idle_min=8  tier=l5
    passive_5h_util=0.42  passive_7d_util=0.18
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
