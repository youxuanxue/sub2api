# AWS prod → Tokyo 切流交接清单

> **历史记录。** Tokyo 已于 2026-10-23 删除，下文所有 `i-045f062ce33e1a5af` 命令均已无目标；现网见 [fleet README](../../deploy/hetzner/README.md)。

切流回执时间：2026-10-17 03:13 UTC；下附 PR #2687 初次只读复审证据（约 04:16–04:17 UTC）及后续获批修复回执；隔离恢复于 05:39 UTC 通过。

## 当前结论

**东京已对外提供服务；美国源冻结 standby；源端 drain/停机未做。复审发现的用量 ID 冲突和发布角色权限已修复，历史明细内容校验通过。另发现并修复 pgdump 空任务，S3 回读及隔离恢复验收通过。QA/告警尚未在本轮独立验收，不能宣布迁移全部完成。**

- 活 prod：`i-045f062ce33e1a5af` / `3.114.187.250` / `ap-northeast-1` / hostname `tokenkey-prod-tokyo-20261017`
- 美国历史主机：`23.21.138.205`（冻结副本，不能直接解冻接写）
- 完整回执：[aws-tokyo-cutover-20261017.json](aws-tokyo-cutover-20261017.json)
- 复审修复回执：[aws-tokyo-review-repair-20261017.json](aws-tokyo-review-repair-20261017.json)

## 已完成窗口（获批范围）

批准文本：`批准顺序开切流，直到东京节点提供服务。`（2026-10-17T02:52:58Z）

1. 源端 `ALTER DATABASE … default_transaction_read_only=on` 冻结；writers 停止。
2. 最终 delta 导出并 apply；冻结水位 usage/dedup max `14727151` / `15538078`；delta 各 2214 行。
3. 解 hold，蓝绿激活 `tokenkey-blue` + Caddy/Redis；本机与四域名 `/health` 200。
4. Porkbun A 记录切到 `3.114.187.250`（TTL 300）。
5. 四边 `MAIN_GATEWAY_ALLOWED_CIDR` 热更新含东京 IP；当时仅确认 `tokenkey-pgdump.timer` 已启用。后续发现 service 为 `/usr/bin/true` 空任务，不能将此项当作备份成功，修复见下文。

## 仍须单独批准

- 美国源 drain / 停机 / 删除（当前保持 frozen + PG/Redis，无 80/443 监听）。

## 回滚（DNS 已切）

此前“DNS 改回旧 IP → 解冻美国源”的步骤有误，本节替代该步骤。东京已经产生新写入，美国冻结库不再是当前数据源；其 80/443 也未监听，直接切回会先中断服务，再造成数据回退或双写。

1. 东京保持唯一权威写库。应用故障优先在东京按正常发布流程回退镜像；不能回退业务数据。
2. 若需使用美国主机作为入口，另行批准并先验证其仅转发东京、不启用旧库 writer，入口就绪后才调整 DNS。
3. 若必须把数据库写入迁回美国，须另批反向迁移：隔离东京 writers，导出最终状态，同步并核对余额、凭据、用量及计费去重记录，证明单写者且目标可服务后才切入口。任何阶段失败，保持隔离并按该反向迁移方案恢复。
4. 禁止双写、直接解冻过期源库、用历史 dump 覆盖活库；冻结副本和切流备份继续保留。

## PR #2687 初次只读复审（以下保留修复前证据）

审查 HEAD：`de1bf555fe7d553260d247b8f3375669dfa58907`。该 HEAD 的 preflight 与 CI 通过，不能代替下面的数据和 IAM 验收。

- **用量 ID 冲突（阻塞）：** 冻结水位 `14727151`。约 04:16 UTC，切流后的 1,429 条记录全部使用 `14724938..14726366`；对这个区间按 `id` 分组，确认 1,429 个 ID 在库中重复。此时 `usage_logs_id_seq.last_value=14726366`，低于历史最大 ID。历史 apply 命令的序列修复仅遍历 `relkind='r'`，漏掉分区父表 `relkind='p'`。按 ID 取详情、排序和增量游标均受影响；尚无证据证明丢记录或重复扣费。修复必须包括安全推进序列及已冲突记录/引用的处理，仅 `setval` 不消除现有冲突。
- **发布权限缺口（阻塞）：** GitHub `AWS_OIDC_ROLE_ARN` 为 `tokenkey-gha-us-east-1-error-clustering`。读取该角色全部 inline/attached policy 后，`SsmSendCommandStage0` 只允许美东旧 EC2 和美东 `AWS-RunShellScript`；edge addon 的 `managed-instance/*` 不覆盖东京 EC2。仓库 resolver 已指向东京，需补最小权限并用实际 GHA 角色做只读 SSM 验收。本次 IAM simulator 因缺少 `iam:SimulatePrincipalPolicy` 权限未运行成功，未 dispatch 发布来验证。
- **迁移来源校验错误：** PR 把东京候选仍映射到旧 Hcloud；原 `validate_final` 和 `fresh_host_sync.resolve` 也沿用 Hcloud 来源。本地修正为美国 AWS 来源，并让同步入口拒绝已 serving 的东京库存；本地修正不代表线上安装或 PR 已更新。
- **回滚步骤错误：** 已在本地按上节纠正；任何线上回退仍是单独生产操作。

只读 SSM 证据（ap-northeast-1，除特别注明）：

| 检查 | CommandId |
|---|---|
| 东京运行状态 | `b340e5b2-5a66-4901-bb6d-b5a4d28b98ca` |
| 美国冻结状态（us-east-1） | `f084bb8c-6a2f-42fe-9302-2dbf4bcaa101` |
| 序列和日志 | `7477400a-0754-434b-a6b3-c9966f11bf8e` |
| 切流后 ID 区间、schema | `ea6bd90d-8fcf-439f-8fc9-305b4e888255` |
| 重复 ID、26 个 stub 软删除 | `90805b59-4a54-4d01-b3c7-8fb0293a52de` |
| 历史 apply（只读检查其命令和结果，非本次执行） | `b6a487f3-af13-40fa-9e2a-12c2c0590051` |

已核实四正式域名 DNS/TLS `/health` 为 200、东京 blue 运行 `1.8.301`、美国保持只读且无公网监听、26 个中继 stub 已软删。源端 `tokenkey.service` 是 active 的 oneshot、但 disabled，不能描述为 inactive。

**初次复审的验收缺口：** apply 回执的 `RECONCILE_OK` 只证明当时部分表 count/max 对齐，未证明全内容及序列正确。当时尚未重算历史明细内容指纹，也未证明东京 DLM/QA/告警和隔离备份恢复。后续修复及验收以本节之后的记录为准。

## 获批修复与验证（2026-10-17）

授权及边界见 [修复审批基线](../approved/tokyo-cutover-review-repair-20261017.md)。详细 CommandId、备份 SHA256、CloudFormation change set 和测试证据归档在上方修复回执。

- **用量 ID：已修复。** 将序列只向前推进，对冻结后落入旧 ID 区间的 2,214 条记录重编号；完整原记录和 ID 映射先存入私有 S3。事务内验证所有非 ID 字段及行数不变；复查冲突、重复请求和缺失计费去重记录均为零。未改费用、余额或请求归属。后续切流入口新增实际序列校验，覆盖普通表与分区父表，在开放网络/启用 writer 前拒绝不安全序列；本地代码尚未推送。
- **历史明细：已核对。** 9 月 1 日 UTC 至冻结水位的 6,767,705 条记录，源库与东京的逐行全内容 SHA256 汇总全部一致；另核对 UTC 9 月 1 日前的 20,310 条记录，覆盖北京时间 9 月 1 日起点，合计验证冻结时全部 6,788,015 条。见 [窗口指纹](aws-tokyo-history-verification-20261017.csv)。前两轮在大窗口超时，只采纳已经完整输出的窗口，剩余窗口缩小后全部完成；包含三个截止水位之后的空窗口。冻结后的新写入另按 ID、请求去重与计费去重核验，不与已冻结美国库比较。
- **GitHub OIDC → 东京 SSM：已修复并实测。** 原 OIDC 栈仅原地修改 prod 权限，信任策略不变；新增独立目标区域。实际 GHA [warm 运行 39003765692](https://github.com/youxuanxue/sub2api/actions/runs/39003765692) 成功，且核对远端 SSM invocation 为 Success/0；预拉的仍是在线镜像 `1.8.301`，未部署或重启。
- **备份：已恢复真实任务。** 原 service 为 `/usr/bin/true` 且缺少 S3 授权。通过原备份栈仅改 pgdump 桶策略 principal，保留媒体权限和生命周期；使用共享安装器恢复脚本与两小时 timer。首份备份 1,421,603,926 字节已上传 S3，AES256，服务退出码 0。该 dump 按既有契约不含 usage/ops/QA 行数据，不可当作用量明细备份；05:39:38 UTC 隔离恢复成功，S3 回读与本机备份 SHA256 一致，完整 SQL 恢复及临时资源清理均通过。五张核心表计数与在线采样一致；去重表恢复快照为 15,497,015 条，较晚的在线采样为 15,497,184 条，期间线上持续写入，不能将两者当作同一时点的逐表一致性比对。
- **EBS 快照：已核实。** 东京 DLM 启用，每日快照保留 7 份；根盘和数据盘的 03:30 UTC 快照均已完成。仅核对生成状态，本轮未做 EBS 全机恢复演练。

05:40 UTC 最终只读复查：切流后 5,535 条用量全部使用旧水位以上的 ID，冲突、重复请求和缺失计费去重均为零；备份 timer 下一次触发为 06:00:43 UTC。网关、Caddy、PostgreSQL 启动时间未变且重启计数均为 0，公开 `/health` 正常。

本轮修复未更改 DNS、应用镜像或重启在线网关。QA raw 链路与告警送达仍未独立验收；旧美国主机的 drain/停机/删除仍须单独批准。

## 连接

```bash
aws ssm start-session --region ap-northeast-1 --target i-045f062ce33e1a5af
aws ssm start-session --region us-east-1 --target i-022f2553503211775
```
