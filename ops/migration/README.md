# Edge 平台迁移

所有 Edge 固定按 `us5 -> us4 -> us6 -> us3` 迁移。基础设施预检和单 Edge
切换门禁均只读；它们不修改 DNS、账号或 AWS 资源。

创建 candidate 前运行 Fleet 实时预检：

```bash
bash ops/migration/edge-platform-migration-preflight.sh \
  --output "${RUNNER_TEMP:-/tmp}/edge-migration-preflight.json"
```

单 Edge 门禁支持 `candidate`、`plan`、`post-dns` 和 `rollback-ready`。离线 fixture：

```bash
bash ops/migration/edge-platform-cutover-check.sh \
  --phase post-dns \
  --fixture /path/to/collected-observation.json \
  --output "${RUNNER_TEMP:-/tmp}/edge-cutover.json"
```

live 模式显式读取两个平台的 SSM 身份，并要求 `--context` 提供本轮真实模型
smoke、源端基线、告警恢复和飞书投递结果；这些不能由工具猜测：

```bash
bash ops/migration/edge-platform-cutover-check.sh \
  --phase plan \
  --edge-id us5 \
  --source-ip 32.185.163.163 \
  --target-ip <candidate-eip> \
  --candidate-observation-started-at <iso-8601> \
  --context /path/to/current-cutover-context.json \
  --output "${RUNNER_TEMP:-/tmp}/us5-plan.json"
```

`context` 是脱敏 JSON 对象，只保留 `rollback_ipv4`、`baseline`、`alerts` 和
`target.oauth_model_smoke_ok`。每个阶段都重新采集，不使用仓库快照；任一缺失信号
或 blocker 使命令非零退出。

# usage_logs 日分区切换

该目录的 `usage_logs_daily_partition.py` 是唯一生产入口。它不会由应用启动或 migration
runner 自动执行，也不会复制历史明细。

先并发创建普通 `(request_id, api_key_id)` 查询索引，再在线准备并验证固定上界 CHECK：

```bash
python3 ops/migration/usage_logs_daily_partition.py prepare \
  --receipt /path/to/usage-prepare-receipt.json \
  --confirm tokenkey-prod-usage-daily-prepare-v1
```

如果 receipt 写入失败或决定取消 cutover，先用 `status` 读取
`legacy_upper_exclusive`，再用与该上界精确绑定的确认串撤销 CHECK：

```bash
python3 ops/migration/usage_logs_daily_partition.py abort \
  --receipt /path/to/usage-abort-receipt.json \
  --legacy-upper-exclusive '<legacy_upper_exclusive>' \
  --confirm 'tokenkey-prod-usage-daily-abort-v1:<legacy_upper_exclusive>'
```

`abort` 只允许在尚未 cutover、且 CHECK 带有 operator ownership 标记时执行；普通查询索引保留。

准备 receipt 会给出绑定上界的 `required_cutover_confirmation`。维护窗口内原样使用该值执行
短 catalog cutover：

```bash
python3 ops/migration/usage_logs_daily_partition.py cutover \
  --prepare-receipt /path/to/usage-prepare-receipt.json \
  --cutover-receipt /path/to/usage-cutover-receipt.json \
  --confirm '<required_cutover_confirmation>'
```

锁等待超过 5 秒、上界过期、CHECK 未验证、出现未知 incoming FK、父表仍有全局唯一索引或
切换后 legacy 行数小于 prepare receipt、或父表行数小于 legacy 行数时均 fail closed。账务幂等
仍由 `usage_billing_dedup` 负责。

## 分区维护一次性入口

`data_layer_partition_maintenance.py` 只面向固定的 `us-east-1` / `tokenkey-prod-stage0`，
不接受 target、instance、script 或 command 参数。steady state 下日常分区维护由
`OpsCleanupService` 负责；本入口仅在 cutover 后补洞或覆盖漂移时使用，每次仍须显式确认串与
独立审批。

命令形状固定为：

```bash
python3 ops/migration/data_layer_partition_maintenance.py run \
  --receipt /path/to/partition-maintenance-receipt.json \
  --confirm tokenkey-prod-partition-maintenance-v1
```

controller 只发送一次固定 SSM 命令，并轮询同一个 `CommandId`。只有远端严格分区维护、覆盖验证和
成功 heartbeat 全部完成后，才会原子创建本地 receipt；已有 receipt 不会被覆盖。该入口不授权删除、
cleanup release、部署、重启或配置修改。
