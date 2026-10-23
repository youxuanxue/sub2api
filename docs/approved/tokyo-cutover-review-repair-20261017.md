---
title: Tokyo cutover review repair
status: archived
approved_by: user (conversation)
approved_at: 2026-10-17
superseded_by: ../ops/tokyo-retirement-runbook.md
scope: repair remaining PR 2687 usage ID and Tokyo OIDC SSM gaps without gateway downtime
---

# 东京切流复审修复

> **已归档（2026-10-23）**：Tokyo 已删除，本文所修目标均已不存在；退役记录见 [Tokyo 退役 runbook](../ops/tokyo-retirement-runbook.md)。

审批锚点：用户在收到剩余缺口（用量 ID 冲突、东京 SSM 发布权限）后明确要求“修复剩下的缺口”。本次按该范围实施；不切流、不停网关、不变更应用镜像，不合并 PR。

## 用量 ID

冻结 max ID 为 14727151；遗漏分区父表序列导致 14724938..14727151 与新记录冲突。只向前推进序列，然后只为冻结时点 2026-10-17T02:54:43.473757Z 后的冲突记录重新分配 ID，范围最多 2214 行。费用、余额、请求归属及其他字段均不变。

实现：`ops/migration/repair-tokyo-usage-20261017.sh` 和两个同目录 SQL 文件。先校验主机、序列定义；短 sequence 锁将 nextval 与推进串行化。每次操作有 250ms 锁超时，重编号语句有 5s 超时。完整原记录及 old/new ID 映射保存到宿主私有目录和既有私有 S3 桶。重编号前校验备份内容摘要，事务内比较每行所有非 ID 字段及行数；出现 billing ledger 引用、外键或未知逻辑引用时拒绝，不猜关联。失败事务回滚；序列跳号保留，禁止向后重置。重跑只处理仍在旧范围的记录。

真实 PostgreSQL 回归覆盖：重复 ID 修复、历史保留、非 ID 内容不变、重复执行、备份变更拒绝、账本引用拒绝、序列不倒退及并发 nextval 串行化。上线后复核冲突为零、新增 ID 超过旧水位、请求计费去重记录及网关健康。

## OIDC

原 `tokenkey-cicd-oidc` 栈仍留美东区域；增加独立 `TargetInstanceRegion` 参数，prod SendCommand 仅允许东京实例和东京 AWS-RunShellScript，区域读取权限覆盖旧栈和新 prod 区域。信任策略不变；不授权通配 EC2 写操作。

通过 `ops/stage0/retarget_prod_oidc.py` 把最小修正渲染到 live get-template，保留无关 live 策略。先创建并检查 change set，仅允许 ClusteringRole 原地修改；保存旧模板和参数。失败按 CFN 回滚；成功后核对真实 IAM，再用既有 GHA warm 工作流对当前运行标签验证 OIDC→东京 SSM。该验证只预拉已存在镜像，不启动/重建容器。

## 回执

修复备份前置条件时发现东京 pgdump service 仍是 `/usr/bin/true` 占位任务，且备份桶仍授权旧主机。作为本次迁移缺口修复，新增 `PgdumpInstanceRoleArn` 参数仅更新既有备份桶的 prod 备份前缀授权，不修改媒体权限或桶生命周期；通过现有 `pg_dump_refresh_via_ssm.sh` 恢复正式脚本和两小时 timer。先备份原 units、确认任务空闲与空间充足；安装不重启网关。验证真实备份生成、离机对象与隔离恢复，禁止把 timer active 当作备份成功。

修复前证据及缺口见 [交接清单](../ops/aws-tokyo-cutover-handoff-20261017.md)。本文件记录授权与执行边界；实际结果以追加回执为准，不以本文件存在代替验收。
