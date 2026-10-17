# AWS prod → Tokyo 切流交接清单

切流回执时间：2026-10-17 03:13 UTC；下附 PR #2687 只读复审证据（约 04:16–04:17 UTC）。

## 当前结论

**东京已对外提供服务；美国源冻结 standby；源端 drain/停机未做。迁移尚未通过复审：用量 ID 冲突及发布角色权限仍须处理。**

- 活 prod：`i-045f062ce33e1a5af` / `3.114.187.250` / `ap-northeast-1` / hostname `tokenkey-prod-tokyo-20261017`
- 美国历史主机：`23.21.138.205`（冻结副本，不能直接解冻接写）
- 完整回执：[aws-tokyo-cutover-20261017.json](aws-tokyo-cutover-20261017.json)

## 已完成窗口（获批范围）

批准文本：`批准顺序开切流，直到东京节点提供服务。`（2026-10-17T02:52:58Z）

1. 源端 `ALTER DATABASE … default_transaction_read_only=on` 冻结；writers 停止。
2. 最终 delta 导出并 apply；冻结水位 usage/dedup max `14727151` / `15538078`；delta 各 2214 行。
3. 解 hold，蓝绿激活 `tokenkey-blue` + Caddy/Redis；本机与四域名 `/health` 200。
4. Porkbun A 记录切到 `3.114.187.250`（TTL 300）。
5. 四边 `MAIN_GATEWAY_ALLOWED_CIDR` 热更新含东京 IP；`tokenkey-pgdump.timer` 已启用。

## 仍须单独批准

- 美国源 drain / 停机 / 删除（当前保持 frozen + PG/Redis，无 80/443 监听）。

## 回滚（DNS 已切）

此前“DNS 改回旧 IP → 解冻美国源”的步骤有误，本节替代该步骤。东京已经产生新写入，美国冻结库不再是当前数据源；其 80/443 也未监听，直接切回会先中断服务，再造成数据回退或双写。

1. 东京保持唯一权威写库。应用故障优先在东京按正常发布流程回退镜像；不能回退业务数据。
2. 若需使用美国主机作为入口，另行批准并先验证其仅转发东京、不启用旧库 writer，入口就绪后才调整 DNS。
3. 若必须把数据库写入迁回美国，须另批反向迁移：隔离东京 writers，导出最终状态，同步并核对余额、凭据、用量及计费去重记录，证明单写者且目标可服务后才切入口。任何阶段失败，保持隔离并按该反向迁移方案恢复。
4. 禁止双写、直接解冻过期源库、用历史 dump 覆盖活库；冻结副本和切流备份继续保留。

## PR #2687 只读复审（未执行生产修复）

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

**验收缺口：** apply 回执的 `RECONCILE_OK` 只证明当时部分表 count/max 对齐，未证明全内容及序列正确。本次未重算 9 月 1 日至今历史明细的全内容指纹，也未证明新东京 DLM/QA/告警和隔离备份恢复均完成验收。不能仅据健康检查宣布迁移全部完成。

## 连接

```bash
aws ssm start-session --region ap-northeast-1 --target i-045f062ce33e1a5af
aws ssm start-session --region us-east-1 --target i-022f2553503211775
```
