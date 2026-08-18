# usage_logs 日分区切换

`usage_logs_daily_partition.py` 是 prod 与 Edge 共用的唯一 operator。它不会由应用启动或
migration runner 自动执行，也不会复制历史明细。每个命令都必须显式指定 `--target prod` 或
`--target edge:<id>`；confirmation、远端 receipt 和本地 receipt 均与同一 target 绑定，不能跨节点复用。

先读取目标状态，再并发创建普通 `(request_id, api_key_id)` 查询索引，并在线准备及验证固定上界
CHECK。以下以 `edge:us3` 为例：

```bash
python3 ops/migration/usage_logs_daily_partition.py status \
  --target edge:us3

python3 ops/migration/usage_logs_daily_partition.py prepare \
  --target edge:us3 \
  --receipt /path/to/usage-prepare-receipt.json \
  --confirm tokenkey-edge-us3-usage-daily-prepare-v1
```

如果 receipt 写入失败或决定取消 cutover，使用与 target 和 receipt 上界精确绑定的确认串撤销
CHECK：

```bash
python3 ops/migration/usage_logs_daily_partition.py abort \
  --target edge:us3 \
  --receipt /path/to/usage-abort-receipt.json \
  --legacy-upper-exclusive '<legacy_upper_exclusive>' \
  --confirm 'tokenkey-edge-us3-usage-daily-abort-v1:<legacy_upper_exclusive>'
```

`abort` 只允许在尚未 cutover、且 CHECK 带有 operator ownership 标记时执行；普通查询索引保留。

prepare receipt 会给出绑定 target 与上界的 `required_cutover_confirmation`。维护窗口内原样使用该值
执行短 catalog cutover，随后可重复验证：

```bash
python3 ops/migration/usage_logs_daily_partition.py cutover \
  --target edge:us3 \
  --prepare-receipt /path/to/usage-prepare-receipt.json \
  --cutover-receipt /path/to/usage-cutover-receipt.json \
  --confirm '<required_cutover_confirmation>'

python3 ops/migration/usage_logs_daily_partition.py verify \
  --target edge:us3 \
  --prepare-receipt /path/to/usage-prepare-receipt.json
```

锁等待超过 5 秒、上界过期、CHECK 未验证、出现未知 incoming FK、父表仍有全局唯一索引、target
与 receipt 不一致、切换后 legacy 行数小于 prepare receipt，或父表行数小于 legacy 行数时均 fail
closed。账务幂等仍由 `usage_billing_dedup` 负责。

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
