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
`aws` / `aws-fresh`（Tokyo，2026-10-23 退役）/ `hetzner` 均已退役拒绝。
若 prod 已失联，在线解析失败是预期保护；从舰队清单读取身份后，以腾讯控制台/TAT 只读核对实例与磁盘。

## 恢复资产

| 资产 | 来源与边界 |
|---|---|
| PostgreSQL | pgBackRest 全量/增量/WAL → COS；恢复到**新盘**后校验，禁止覆盖活库 |
| Redis | AOF everysec；迁机可新建，不保证会话/限流状态可回放 |
| 入口 TLS / Caddy 材料 | COS `prod/host-config/` 明文 tar.gz（见 `ops/archive/README.md`）；应用/数据机全量 `.env` **不**打包 |
| 整机/卷快照 | **不做**常备策略（见 [`declined.md`](../../docs/decisions/declined.md)） |
| Tokyo 历史库 | S3 `prod/postgres` timeline 1 只读；禁止写回 |
| 历史逻辑备份 | 旧 pgdump 退役保留期内可回查；可能不含 usage 明细 |
| QA raw | 本地 24h + node1；不恢复 S3 上传（见 declined） |

实际 RPO 由远端连续可恢复 WAL 与成功备份链决定，不能只用最新对象时间。
当前验收与缺口见 [物理备份](../../ops/stage0/physical-backup.md)；恢复后仍须核对账务与明细，
恢复耗时以独立演练为准。旧 pgdump 不再提供持续更新的恢复点。

## 应用发布故障

先用 `ops/observability/run-probe.sh --target prod` 读取状态，核对活动色、Caddy 上游和
镜像摘要。镜像回滚通过原 prod dispatcher（`prod-rolling-deploy.yml` /
`dispatch-prod-deploy.sh`）发布上一已知良好 tag；见
[`design-prod-entry-multi-app`](../../docs/approved/design-prod-entry-multi-app.md)。
同机蓝绿仅应急：`ops/stage0/deploy_via_ssm_bluegreen.sh` +
`STAGE0_ALLOW_LEGACY_BLUEGREEN=1`。不能直接启动另一套单容器 compose，
也不能通过 DNS 把写流量送回已冻结/退役旧库。

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
