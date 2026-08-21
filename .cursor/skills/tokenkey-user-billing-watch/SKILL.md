---
name: tokenkey-user-billing-watch
description: >-
  Read-only TokenKey production user billing/usage/error watch. Use for active-user monitoring, 盯盘, 30-minute reporting loops, anomaly notification, or distinguishing client noise from real metering/system issues.
---

# TokenKey：按用户用量/计费/错误盯盘（新会话一键启动）

把"先快速查当前活跃用户清单，再盯这些用户的请求、用量、错误并按需推送"固定成稳定的**只读**流程，让任何新会话敲 `/tokenkey-user-billing-watch` 即可起盘，无需手敲整段 spec。

权威纪律以仓库根 `CLAUDE.md` 为准。本 skill **只读**：只经 `run-probe.sh` 下发纯 SELECT 的 probe 脚本。任何写配置、改限额、重启、部署都必须另行显式确认。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审：

- **机械化（脚本承载，prompt 不重写）**：活跃用户发现、取数、字段解析、image/video 判别、窗口/用户参数化、SQL 注入守卫——全在 `ops/observability/probe-user-billing-watch.sh`。倍率与环比算术也是机械的：倍率按 `actual_cost / total_cost`；环比按上一窗读数相减。
- **真判断（留给 prompt / 本 skill）**：一条错误是客户端侧噪声还是系统异常（§4 判别法）、是否触发推送（§3）。仅此二者。

## §1 启动（每次起盘跑这一条）

先快速查活跃用户清单，再用这份清单跑 billing watch；默认以当前 `users.status = 'active' AND deleted_at IS NULL` 的整份清单为准。若你要盯一个固定子集，才手动传 `USER_IDS=...` 覆盖。

在仓库根（或当前 worktree 根）运行：

```bash
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-user-billing-watch.sh \
  --env WINDOW_MINUTES=30 \
  --comment "active-user billing watch"
```

- `probe-user-billing-watch.sh` 会先读当前活跃用户清单，再继续请求 / 用量 / 错误统计；若活跃用户为空，直接报空窗。
- 需要盯固定子集时，才额外传 `--env USER_IDS=1,6,16,38` 覆盖默认活跃清单。
- 若 `run-probe.sh` 在本机 `aws/pyexpat` 启动阶段就失败（macOS/Homebrew 常见），先运行：`python3 scripts/checks/check-local-aws-pyexpat.py --apply`，再重试本命令。
- **失败如实报告，绝不编数**：`status!=Success` / 非零退出 / SSM 传输错误时，直接报失败与原因，不臆造任何数字。

## §2 报告格式（固定）

表格优先、中文、精简。**只报告监控窗口内有请求或有用量的用户**；`reqs=0` 且 `total_cost=actual_cost=0` 的 active 用户不进表，除非它命中了 §3 的“流量归零”告警条件。每个入表 user 一行，含：

- 成功请求数（`reqs` / `billed_reqs` / `zero_cost_reqs`）。
- 计费 `total_cost` vs 实际 `actual_cost`，标注倍率 `actual_cost / total_cost`（`total_cost=0` 时写 `N/A`，不要反向用 `total/actual`）。
- 真客户端失败数 + 主错误类型（尤其空池 429 / 502 / 上游 4xx-5xx）；若某错误类型窗口内 `n > 10`，额外列出对应 key、分组、错误模型，以及该 key 是否为 universal key。
- 主力模型 Top3（按请求数；成本结构异于请求数时可补一句按成本的首位）。
- **与对话里上一窗的环比**：请求 / 成本 / 错误各给 ↑↓= 箭头。

末尾一句**判断**：推送 or 不推送 + 一句理由。

## §3 推送判据（仅这四类才 PushNotification）

1. 某用户**流量归零**：窗口内 0 成功，且 `last_success_utc` / `last_error_utc` 仍在近期（上一窗或最近几个窗还能看到活动）。这类用户即使本窗无请求/无用量，也要作为例外单独报出并按异常判断是否推送。`status=active` 但长期无 last-seen 的空闲账号不进表、不推送；若需要说明，只在表后一句带过即可。
2. **错误率明显突升**（注意：总量骤降导致的比率被动抬高、而错误绝对量没涨，**不算**突升）。
3. **成本异常飙升**（区分真实高价模型消费 vs 异常；前者不推）。
4. **新出现的错误类型**（§4 三条规则都覆盖不到的新指纹）。

常规读数只在对话内汇报，不打扰。

## §4 客户端侧 vs 系统异常的判别法（固化"怎么判"，不固化"哪几条"）

核心是**结构指纹分类**——靠 schema 级字段判别，长期稳定；具体命中的模型名/把数是 point-in-time、会变，**不写死成白名单**（写死会沉淀为错误记忆）。

**判别规则（durable，照此现场判）：**

| 指纹 | 含义 | 处置 |
|---|---|---|
| `error_phase=routing` + `account_id=null` + `error_owner=client` | 错组 key 误投等客户端路由过错（如 newapi 长尾模型用 anthropic 组 key 发 `/v1/messages`） | 客户端侧，**非系统**，不推送 |
| `error_phase=routing` + `error_owner=platform` | 空池 / 镜像 edge 下游容量拒绝（计入 SLA，但专用 `routing_capacity_rejection` 计数告警仍隔离风暴） | 容量侧；单点少量可不推送，**突升**按系统异常推送 |
| `status_code=200` + `upstream_status_code∈{429,502,5xx}` | recovered-200：重试已成功，用户侧无感 | 不推送 |
| `status_code=200` + `msg` 仅含 `cc_environment_stripped` / `cc_geo_stego_normalized`（或 `request_normalized` 审计类） | v1.8.64+ Anthropic CC prompt normalize 预期改写；`gateway.anthropic_request_normalized` 为 canonical 审计 | 不推送 |
| `error_phase∈{request,upstream}` 的 4xx（`data_inspection_failed` 内容审核 / 退役模型 / prompt too long / 参数错） | 客户端输入或用法问题 | 不推送 |
| `status_code≥500` 真失败（非 recovered） / 空池 429 绝对量**突升** / 流量归零 / 成本飙升 / 上述都不匹配的**新指纹** | 疑似系统异常 | **推送**并简述 |

**当前实例只作锚点、需复核（会变，别照搬）：** 截至最近观察，常见噪声为「某 deepseek/qwen 长尾模型经 anthropic 组 key 误投触发空池 429」「qwen 阿里内容审核 400」。这些**会随模型上下线、客户端改 key 而变化**——新会话先按上表规则现场判，再对照锚点确认是不是同一桩，**不要**把具体模型名当成永久"忽略清单"。

相关记忆：`gateway_empty_pool_429_not_503`（空池 429 四分类，含错组 key 误投）、`project_account_incident_feishu_alert`（`routing_capacity_rejection_count` 专用 P0 仍隔离 error_phase=routing 风暴；SLA 分子已含 platform 路由过错）。

## §5 挂 30 分钟循环

起盘后，用 `CronCreate` 挂会话级循环（例：`13,43 * * * *`），prompt 即"重跑 §1 + 按 §2/§3/§4 汇报"。
- **会话级、7 天自动过期**——告知用户，关会话即停；要跨会话长跑需重新起盘（这正是本 skill 的意义：随时一键重起）。
- 或交给通用 `loop` skill 自带调度。

## §6 边界

- 不改 probe 脚本、不建新脚本——执行体已参数化够用。
- 本 skill 全程只读；遇到需要写操作（改限额 / 重启 / 改账号）的诉求，停下显式确认并转交写入面 skill。
