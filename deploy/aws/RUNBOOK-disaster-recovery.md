# TokenKey prod — 恢复入口（现网腾讯美西）

> prod 数据库主恢复入口已更新为 [pgBackRest](../../ops/stage0/physical-backup.md)，
> 新机迁移按 [物理恢复工作流](../../ops/stage0/pgbackrest-host-migration.md)。
> 下文 pgdump 是历史隔离恢复参考，不代表仍有定时新备份；cn-prod 独立备份不变。

现网身份、代理和保留依赖的唯一入口是 [舰队清单](../hetzner/README.md)。
旧 AWS / Hcloud 主机已回收，不能把旧 IP、旧 CFN 输出或冻结库当作可用回滚目标。
此前美东/Hetzner 专用操作保留在 Git 历史，不再是现网命令模板。

## 先确认目标与恢复点

```bash
python3 ops/stage0/resolve_prod_ssm_target.py --target auto --format json
```

解析器核对腾讯矩阵中的 Hybrid `mi-*`、SSM 区域与在线名称；矩阵不可部署时失败关闭，不回退 Tokyo。
冻结 Tokyo `aws-fresh` 只读须显式 `ALLOW_FROZEN_TOKYO_TARGET=1`；`aws` / `hetzner` 已退役拒绝。
若 prod 已失联，在线解析失败是预期保护；从舰队清单读取身份后，以腾讯控制台/TAT 只读核对实例与磁盘。

## 恢复资产

| 资产 | 来源与边界 |
|---|---|
| PostgreSQL、Redis、Caddy 与主机配置 | 腾讯系统盘上的 LUKS mapper（双挂 `/var/lib/tokenkey`、`/srv/tokenkey-rehearsal`）；解锁见 [unlock 契约](../../docs/ops/tencent-unlock-contract.md) |
| 全盘恢复点 | **当前无**（腾讯机无 EBS/DLM；快照策略为开放项）。东京 DLM 只覆盖冻结 Tokyo |
| 数据库主备份 | pgBackRest 全量/增量/WAL；整 cluster 含账本、usage 和索引；恢复到新盘后校验 |
| 历史逻辑备份 | 私有 S3 pgdump 已进入退役保留；核对所选文件实际覆盖与时间，旧备份可能不含 usage 明细 |
| 已退役节点的历史库 | [独立 S3 冻结归档](../../docs/ops/aws-retirement-20261017.md)，用于运营回查，不覆盖在线库 |
| 密钥与环境配置 | 当前卷及受保护的 SSM/恢复材料；按真实备份来源核对，旧路径名称不等于无用 |
| Redis | 用户已选 AOF everysec；恢复时核对实际持久化配置与数据，不能假定所有 Redis 状态均可丢弃 |
| QA raw | 历史不迁；S3 上传当前暂停，按 [QA 恢复分界](../../ops/qa/README.md#生产存储与恢复分界)处理 |

实际 RPO 由远端连续可恢复 WAL 与成功备份链决定，不能只用最新对象时间。
当前验收与缺口见 [物理备份](../../ops/stage0/physical-backup.md)；恢复后仍须核对账务与明细，
恢复耗时以独立演练为准。旧 pgdump 不再提供持续更新的恢复点。

## 应用发布故障

先用 `ops/observability/run-probe.sh --target prod` 读取状态，核对活动色、Caddy 上游和
镜像摘要。镜像回滚通过原 prod dispatcher 发布上一已知良好 tag，遵循
[蓝绿发布契约](../../docs/approved/deploy-stage0-workflow.md)。
不能直接启动另一套单容器 compose，也不能通过 DNS 把写流量送回已冻结/退役旧库。

## 主机或数据卷故障

1. 只读核对当前实例、LUKS 挂载、物理备份链和恢复材料；给出候选恢复点及可能丢失窗口。
2. 按 [新机替换流程](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)准备目标云的独立身份。
   任何新建、挂载、恢复、停写和切流执行须符合该次授权；恢复点取舍由运营确认。
3. 在隔离节点恢复，验证数据可读、关键记录/使用明细覆盖、容器健康与密钥完整性。
   保留无业务写入、无 OAuth 刷新和无 timer 上传边界，QA 上传暂停策略随恢复保持。
4. 源端仍可用时，沿 standby 追 WAL → 隔离所有源写者 → 最终 WAL 入仓/重放 → 对账 → promote → 切流流程。
   新机使用新清单，不修改退役行来绕过 `retired` 门禁。
5. 基本服务和正式入口验证通过后，更新现网身份与部署/监控目标，记录恢复点、缺口和验收。
   美西 `usw-eval*` / 国内 `cn-ip*` 出口代理属于独立现役依赖，不随 prod 恢复自动回收或换绑。

已有写入的新库不能被旧冻结整库覆盖。历史回查恢复到独立数据库；定点补数是另外的
数据修复任务，不能混入主机恢复或自动修改账单/余额。
