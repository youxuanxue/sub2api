---
name: tokenkey-user-billing-watch
description: >-
  Read-only TokenKey production user billing/usage/error watch. Use for active-user monitoring, 盯盘, 30-minute reporting loops, anomaly notification, or distinguishing client noise from real metering/system issues.
---

# TokenKey：按用户用量/计费/错误盯盘

一键起盘：分别从腾讯美西 `prod`、腾讯北京 `cn-prod` 取请求/用量/错误并按 §3 判定。全程**只读**；写操作转交写入面 skill。

脚本已计算活跃用户、环比、24h 基线及用户侧失败归因；报告只做 §4 判断和 §3 推送判断。

## §1 起盘

两站分别执行，余额、用量和用户 ID 不合并。

```bash
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-user-billing-watch.sh \
  --env WINDOW_MINUTES=30 \
  --comment "billing watch tencent-usw-prod" \
  --compressed-output
```

cn-prod 应用在腾讯北京。probe 通过 AWS SSM Hybrid 下发，身份取 `deploy/domestic/prod-target.json`，不依赖运维机公网 IP，也不需要开放公网 SSH 22：

```bash
bash ops/observability/run-probe.sh \
  --target cn-prod \
  --script ops/observability/probe-user-billing-watch.sh \
  --expected-instance-id lhins-6qmb3fd6 \
  --env WINDOW_MINUTES=30 \
  --comment "billing watch cn-prod" \
  --compressed-output
```

SSH 仅用于 SSM agent 的一次性 bootstrap 或显式恢复；SSH、SSM、远端或压缩帧失败均不使用部分结果。

- 默认盯 `users.status='active' AND deleted_at IS NULL` 全量；固定子集才加 `--env USER_IDS=1,6,16`。
- `--compressed-output` 防 SSM/SSH 截断；校验失败不用部分结果。
- `--target prod` 经 `resolve_prod_ssm_target.py` 解析当前 Hybrid/区域，身份见 [舰队清单](../../../deploy/hetzner/README.md)（路径名历史遗留）。`auto` → 腾讯美西；`aws` / `aws-fresh` / `hetzner` 均已退役拒绝。
- `--target cn-prod` 指向腾讯北京上的 cn-prod 库；通过 AWS SSM Hybrid 下发，不读取美西 prod 数据库。`106.75.29.139` 只转发。
- 本机 `aws/pyexpat` 启动失败（macOS 常见）：先 `python3 scripts/checks/check-local-aws-pyexpat.py --apply`。
- **失败如实报告**：`status!=Success` / 非零退出 / 传输错误 → 报失败与原因，绝不编数。

## §2 读数口径

- 环比：读 `delta_reqs_pct` / `delta_cost_pct` / `delta_n_pct`；上一窗为 0 时字段为 `null`，写"新出现"。
- `last_success_utc` / `last_error_utc` 只覆盖最近 7 天。更早为 `null`，按长期空闲处理，不推送。
- 突升/飙升：拿当前窗对 `baseline: ...trailing 24h` 的 `avg`/`max` 比，不拿两个点瞪着猜。
- 倍率：`actual_cost / total_cost`，内部口径，默认不进表。
- **用户侧失败**：只看 `errors: user-facing failures by model/account`。判据与告警/SLA 的同一 owner（`backend/internal/repository/ops_repo_user_visible_failure_tk.go`）对齐：`>=400`、排除 499 与 `context cancel` 文本（调用方挂断的两种形态）、用户归属只看 `user_id`（已删除 key 归因连同写入方一并移除，见 `docs/approved/ops-error-logs-column-contract.md`；`user_id` 为空的行不归任何人）。**不要在报告侧另立一套口径**。每行自带模型、承接账号（`account_name`/`account_platform`；`account_status_now` 是账号**此刻**状态而非失败当时，不要当成失败原因叙述；`account_soft_deleted=true` 表示账号已软删、状态更不可信）、分组与 key、`root_cause_sample`。`account_id=null` 表示请求没进池（routing 阶段）。这一节是"用户终端到底遇到了什么"的唯一取数口径，**必须报出模型 + 承接账号 + 根因**，不要只报个错误码。

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
| `error_phase=routing` + `error_owner=platform` | 账号池容量拒绝 | 容量侧；少量不推，**突升**按系统异常推 |
| `error_phase∈{request,upstream}` 4xx（内容审核 / 退役模型 / prompt 超长 / 参数错） | 客户端输入或用法问题 | 不推送 |
| `status_code>=500` 真失败 / 空池 429 绝对量突升 / 流量归零 / 成本飙升 / 新指纹 | 疑似系统异常 | **推送**并简述模型+账号+根因 |

**锚点（会变，需复核，别当忽略清单）**：近期常见噪声为「长尾 deepseek/qwen 模型经 anthropic 组 key 误投触发空池 429」「qwen 内容审核 400」。先按上表判，再对照锚点确认是不是同一桩。

## §5 循环

`CronCreate` 挂会话级循环（例 `13,43 * * * *`），prompt 即"分别重跑腾讯美西 prod 与腾讯北京 cn-prod 的 §1，按 §2/§3/§4 分节点汇报"。会话级、7 天过期——告知用户，关会话即停。

## §6 边界

新增取数一律加进现有 `probe-user-billing-watch.sh`，**不建新脚本**；改完必须跑一次真实 probe 验证 SQL 与返回结构。
