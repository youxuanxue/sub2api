# TokenKey Tokyo prod — 恢复入口

现网身份、代理和保留依赖的唯一入口是 [舰队清单](../hetzner/README.md)。
旧 AWS / Hcloud 主机已回收，不能把旧 IP、旧 CFN 输出或冻结库当作可用回滚目标。
此前美东/Hetzner 专用操作保留在 Git 历史，不再是现网命令模板。

## 先确认目标与恢复点

```bash
python3 ops/stage0/resolve_prod_ssm_target.py --target aws-fresh --format json
```

解析器核对清单中的 EC2 ID、SSM 区域与在线 hostname。`auto` 默认同一路径；
`PROD_SSM_TARGET=aws` 和已退役的 `hetzner` 身份拒绝。若 prod 已失联，在线解析失败是
预期保护；从舰队清单读取身份后，以只读 AWS API 核对实例/卷/快照，不能改用旧 standby。

## 恢复资产

| 资产 | 来源与边界 |
|---|---|
| PostgreSQL、Redis、Caddy 与主机配置 | 当前 prod 的独立 EBS 数据卷；先核对卷 ID、挂载和所属实例 |
| 全盘恢复点 | 东京 DLM 根盘/数据盘快照；选择已完成、同一批次快照，验证恢复可读性 |
| 核心数据库逻辑备份 | 私有 S3 pgdump，常规定时约每两小时；排除 usage/运维日志/QA raw，不能当作完整使用明细备份 |
| 已退役节点的历史库 | [独立 S3 冻结归档](../../docs/ops/aws-retirement-20261017.md)，用于运营回查，不覆盖在线库 |
| 密钥与环境配置 | 当前卷及受保护的 SSM/恢复材料；按真实备份来源核对，旧路径名称不等于无用 |
| Redis | 用户已选 AOF everysec；恢复时核对实际持久化配置与数据，不能假定所有 Redis 状态均可丢弃 |
| QA raw | 历史不迁；S3 上传当前暂停，按 [QA 恢复分界](../../ops/qa/README.md#生产存储与恢复分界)处理 |

实际 RPO 由最近成功的备份时间与覆盖范围决定。每日 DLM 的 usage 恢复点可能落后约一天；
不能用核心库 pgdump 的两小时周期承诺 usage 明细 RPO。恢复耗时以隔离演练为准。

## 应用发布故障

先用 `ops/observability/run-probe.sh --target prod` 读取状态，核对活动色、Caddy 上游和
镜像摘要。镜像回滚通过原 prod dispatcher 发布上一已知良好 tag，遵循
[蓝绿发布契约](../../docs/approved/deploy-stage0-workflow.md)。
不能直接启动另一套单容器 compose，也不能通过 DNS 把写流量送回已冻结/退役旧库。

## 主机或数据卷故障

1. 只读核对当前实例、数据卷、最新 DLM 批次、逻辑备份和恢复材料；给出候选恢复点及可能丢失窗口。
2. 按 [新机替换流程](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)准备独立 EC2 身份。
   任何新建、挂载、恢复、停写和切流执行须符合该次授权；恢复点取舍由运营确认。
3. 在隔离节点恢复，验证数据可读、关键记录/使用明细覆盖、容器健康与密钥完整性。
   保留无业务写入、无 OAuth 刷新和无 timer 上传边界，QA 上传暂停策略随恢复保持。
4. 源端仍可用时，沿标准预同步 → 停写 → 最终增量 → 对账 → 激活 → 切流流程。
   新机使用新清单，不修改退役行来绕过 `retired` 门禁。
5. 基本服务和正式入口验证通过后，更新现网身份与部署/监控目标，记录恢复点、缺口和验收。
   日本出口代理属于独立现役依赖，不随 prod 恢复自动回收或换绑。

已有写入的新库不能被旧冻结整库覆盖。历史回查恢复到独立数据库；定点补数是另外的
数据修复任务，不能混入主机恢复或自动修改账单/余额。
