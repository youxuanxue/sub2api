---
name: tokenkey-user-billing-watch
description: >-
  Read-only TokenKey production user billing/usage/error watch. Use for active-user monitoring, 盯盘, 30-minute reporting loops, anomaly notification, or distinguishing client noise from real metering/system issues.
---

# TokenKey：按用户用量/计费/错误盯盘

一键起盘：自动发现活跃用户 → 取请求/用量/错误 → 按 §3 判是否推送。全程**只读**（纯 SELECT，经 `run-probe.sh` 下发）。要改限额/账号/重启/部署，停下显式确认并转交写入面 skill。纪律以仓库根 `CLAUDE.md` 为准。

**脚本已算完的，prompt 不要重算**：活跃用户发现、image/video 判别、环比、24h 基线、用户侧失败的模型/账号/根因关联，全在 `ops/observability/probe-user-billing-watch.sh` 的 SQL 里。直接读字段，禁止自己做减法或跨表对照。留给判断的只有两件：错误是客户端噪声还是系统异常（§4）、数字是否值得推送（§3）。

## §1 起盘

```bash
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-user-billing-watch.sh \
  --env WINDOW_MINUTES=30 \
  --comment "active-user billing watch" \
  --compressed-output
```

- 默认盯 `users.status='active' AND deleted_at IS NULL` 全量；固定子集才加 `--env USER_IDS=1,6,16`。
- `--compressed-output` 防 SSM 截断；压缩帧/校验失败按失败处理，不用部分结果。
- 本机 `aws/pyexpat` 启动失败（macOS 常见）：先 `python3 scripts/checks/check-local-aws-pyexpat.py --apply`。
- **失败如实报告**：`status!=Success` / 非零退出 / 传输错误 → 报失败与原因，绝不编数。

## §2 读数口径

- 环比：读 `delta_reqs_pct` / `delta_cost_pct` / `delta_n_pct`；上一窗为 0 时字段为 `null`，写"新出现"。
- 突升/飙升：拿当前窗对 `baseline: ...trailing 24h` 的 `avg`/`max` 比，不拿两个点瞪着猜。
- 倍率：`actual_cost / total_cost`，内部口径，默认不进表。
- **用户侧失败**：只看 `errors: user-facing failures by model/account`。判据与告警/SLA 的同一 owner（`backend/internal/repository/ops_repo_user_visible_failure_tk.go`）对齐：`>=400`、排除 499 与 `context cancel` 文本（调用方挂断的两种形态）、用户归属回退 `deleted_key_owner_user_id`。**不要在报告侧另立一套口径**。每行自带模型、承接账号（`account_name`/`account_platform`；`account_status_now` 是账号**此刻**状态而非失败当时，不要当成失败原因叙述；`account_soft_deleted=true` 表示账号已软删、状态更不可信）、分组与 key、`root_cause_sample`。`account_id=null` 表示请求没进池（routing 阶段）。这一节是"用户终端到底遇到了什么"的唯一取数口径，**必须报出模型 + 承接账号 + 根因**，不要只报个错误码。

报告格式与示例：[references/report.md](references/report.md)。

## §3 推送判据（仅这四类才 PushNotification）

1. **流量归零**：窗口内 0 成功，但 `last_success_utc`/`last_error_utc` 仍在近期。长期无 last-seen 的空闲 active 账号不进表、不推送。
2. **错误绝对量突升**（总量骤降导致的比率被动抬高，**不算**）。
3. **成本异常飙升**（真实高价模型消费不算）。
4. **新指纹**：§4 各行都覆盖不到的错误。

常规读数只在对话内汇报。

## §4 客户端噪声 vs 系统异常

按 schema 级字段现场判（durable）；具体模型名是 point-in-time，**不写死白名单**。

| 指纹 | 含义 | 处置 |
|---|---|---|
| `status_code=200` + `upstream_status_code∈{429,5xx}` | recovered-200，重试兜住，用户无感 | 不推送（但要报出） |
| `status_code=200` + msg 仅 `cc_environment_stripped`/`cc_geo_stego_normalized`/`request_normalized` | Anthropic CC prompt normalize 预期改写 | 不推送 |
| `status_code=499`，或 msg 含 `context cancel` | 调用方自己挂断（`client-closed-499-ssot.md`）的两种形态，非网关故障 | 不推送；已在 probe 侧按 owner 判据过滤 |
| `error_phase=routing` + `account_id=null` + `error_owner=client` | 错组 key 误投等客户端路由过错 | 客户端侧，不推送 |
| `error_phase=routing` + `error_owner=platform` | 空池 / 镜像 edge 容量拒绝 | 容量侧；少量不推，**突升**按系统异常推 |
| `error_phase∈{request,upstream}` 4xx（内容审核 / 退役模型 / prompt 超长 / 参数错） | 客户端输入或用法问题 | 不推送 |
| `status_code>=500` 真失败 / 空池 429 绝对量突升 / 流量归零 / 成本飙升 / 新指纹 | 疑似系统异常 | **推送**并简述模型+账号+根因 |

**锚点（会变，需复核，别当忽略清单）**：近期常见噪声为「长尾 deepseek/qwen 模型经 anthropic 组 key 误投触发空池 429」「qwen 内容审核 400」。先按上表判，再对照锚点确认是不是同一桩。

相关记忆：`gateway_empty_pool_429_not_503`、`project_account_incident_feishu_alert`。

## §5 循环

`CronCreate` 挂会话级循环（例 `13,43 * * * *`），prompt 即"重跑 §1，按 §2/§3/§4 汇报"。会话级、7 天过期——告知用户，关会话即停。

## §6 边界

新增取数一律加进现有 `probe-user-billing-watch.sh`，**不建新脚本**；改完必须跑一次真实 probe 验证 SQL 与返回结构。
