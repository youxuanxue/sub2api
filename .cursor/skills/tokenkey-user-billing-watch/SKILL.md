---
name: tokenkey-user-billing-watch
description: >-
  Read-only TokenKey production per-user billing/usage/error watch — boots the
  recurring 盯盘 in any fresh session. Runs ops/observability/probe-user-billing-watch.sh
  via run-probe.sh (one SSM round-trip, pure SELECT, default USER_IDS=1,16
  WINDOW_MINUTES=30), then reports per-user success counts (reqs/billed/zero_cost),
  metering total_cost vs actual_cost with the multiplier, error counts by
  status/phase, and top-3 models, with window-over-window arrows. Pushes a
  notification ONLY on a real anomaly (traffic to zero / error-rate spike / cost
  spike / a new error fingerprint); routine reads stay in-chat, tables-first,
  Chinese. Teaches the structural-fingerprint method to tell client-side noise
  (wrong-group-key empty-pool 429, recovered-200, content/retired-model 4xx)
  from a true system anomaly, instead of a brittle hardcoded noise list. Use
  when asked to 盯盘 / 启动盯盘 / monitor user usage / 用量监控 / billing watch /
  watch user 1,16, or to arm the 30-min reporting loop.
---

# TokenKey：按用户用量/计费/错误盯盘（新会话一键启动）

把"每 30 分钟盯 user 1/16 的请求、用量、错误并按需推送"固定成稳定的**只读**流程，让任何新会话敲 `/tokenkey-user-billing-watch` 即可起盘，无需手敲整段 spec。

权威纪律以仓库根 `CLAUDE.md` 为准。本 skill **只读**：只经 `run-probe.sh` 下发纯 SELECT 的 probe 脚本。任何写配置、改限额、重启、部署都必须另行显式确认。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审：

- **机械化（脚本承载，prompt 不重写）**：取数、字段解析、image/video 判别、窗口/用户参数化、SQL 注入守卫——全在 `ops/observability/probe-user-billing-watch.sh`。环比算术（箭头）也是机械的，按上一窗读数相减即可。
- **真判断（留给 prompt / 本 skill）**：一条错误是客户端侧噪声还是系统异常（§4 判别法）、是否触发推送（§3）。仅此二者。

## §1 启动（每次起盘跑这一条）

在仓库根（`/Users/xuejiao/Codes/token/tk/sub2api`，或当前 worktree 根）运行：

```bash
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-user-billing-watch.sh \
  --env USER_IDS=1,16 \
  --env WINDOW_MINUTES=30 \
  --comment "user 1,16 30min billing watch"
```

- `USER_IDS` / `WINDOW_MINUTES` 可改（脚本默认 `1,16` / `30`）。user 1 = `admin@tokenkey.dev`，user 16 = `compute@tk.com`（计算所）。
- **失败如实报告，绝不编数**：`status!=Success` / 非零退出 / SSM 传输错误时，直接报失败与原因，不臆造任何数字。

## §2 报告格式（固定）

表格优先、中文、精简。每个 user 一行，含：

- 成功请求数（`reqs` / `billed_reqs` / `zero_cost_reqs`）。
- 计费 `total_cost` vs 实际 `actual_cost`，标注约几倍倍率（`total/actual`）。
- 真客户端失败数 + 主错误类型（尤其空池 429 / 502 / 上游 4xx-5xx）。
- 主力模型 Top3（按请求数；成本结构异于请求数时可补一句按成本的首位）。
- **与对话里上一窗的环比**：请求 / 成本 / 错误各给 ↑↓= 箭头。

末尾一句**判断**：推送 or 不推送 + 一句理由。

## §3 推送判据（仅这四类才 PushNotification）

1. 某用户**流量归零**（活跃用户突然 0 成功）。
2. **错误率明显突升**（注意：总量骤降导致的比率被动抬高、而错误绝对量没涨，**不算**突升）。
3. **成本异常飙升**（区分真实高价模型消费 vs 异常；前者不推）。
4. **新出现的错误类型**（§4 三条规则都覆盖不到的新指纹）。

常规读数只在对话内汇报，不打扰。

## §4 客户端侧 vs 系统异常的判别法（固化"怎么判"，不固化"哪几条"）

核心是**结构指纹分类**——靠 schema 级字段判别，长期稳定；具体命中的模型名/把数是 point-in-time、会变，**不写死成白名单**（写死会沉淀为错误记忆）。

**判别规则（durable，照此现场判）：**

| 指纹 | 含义 | 处置 |
|---|---|---|
| `error_phase=routing` + `account_id=null` + `is_business_limited=true` | 空池 / 错组 key 误投（如 newapi 长尾模型用 anthropic 组 key 发 `/v1/messages`） | 客户端侧，**非系统**，不推送 |
| `status_code=200` + `upstream_status_code∈{429,502,5xx}` | recovered-200：重试已成功，用户侧无感 | 不推送 |
| `error_phase∈{request,upstream}` 的 4xx（`data_inspection_failed` 内容审核 / 退役模型 / prompt too long / 参数错） | 客户端输入或用法问题 | 不推送 |
| `status_code≥500` 真失败（非 recovered） / 空池 429 绝对量**突升** / 流量归零 / 成本飙升 / 上述都不匹配的**新指纹** | 疑似系统异常 | **推送**并简述 |

**当前实例只作锚点、需复核（会变，别照搬）：** 截至最近观察，常见噪声为「某 deepseek/qwen 长尾模型经 anthropic 组 key 误投触发空池 429」「qwen 阿里内容审核 400」。这些**会随模型上下线、客户端改 key 而变化**——新会话先按上表规则现场判，再对照锚点确认是不是同一桩，**不要**把具体模型名当成永久"忽略清单"。

相关记忆：`gateway_empty_pool_429_not_503`（空池 429 四分类，含错组 key 误投）、`project_account_incident_feishu_alert`（这类 business-limited 429 不触发飞书 P0 的结构盲区）。

## §5 挂 30 分钟循环

起盘后，用 `CronCreate` 挂会话级循环（例：`13,43 * * * *`），prompt 即"重跑 §1 + 按 §2/§3/§4 汇报"。
- **会话级、7 天自动过期**——告知用户，关会话即停；要跨会话长跑需重新起盘（这正是本 skill 的意义：随时一键重起）。
- 或交给通用 `loop` skill 自带调度。

## §6 边界

- 不改 probe 脚本、不建新脚本——执行体已参数化够用。
- 本 skill 全程只读；遇到需要写操作（改限额 / 重启 / 改账号）的诉求，停下显式确认并转交写入面 skill。
