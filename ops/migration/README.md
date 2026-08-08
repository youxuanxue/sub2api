# usage_logs 日分区切换

该目录的 `usage_logs_daily_partition.py` 是唯一生产入口。它不会由应用启动或 migration
runner 自动执行，也不会复制历史明细。

先并发创建普通 `(request_id, api_key_id)` 查询索引，再在线准备并验证固定上界 CHECK：

```bash
python3 ops/migration/usage_logs_daily_partition.py prepare \
  --receipt .testing/user-stories/attachments/usage-prepare.json \
  --confirm tokenkey-prod-usage-daily-prepare-v1
```

如果 receipt 写入失败或决定取消 cutover，先用 `status` 读取
`legacy_upper_exclusive`，再用与该上界精确绑定的确认串撤销 CHECK：

```bash
python3 ops/migration/usage_logs_daily_partition.py abort \
  --receipt .testing/user-stories/attachments/usage-abort.json \
  --legacy-upper-exclusive '<legacy_upper_exclusive>' \
  --confirm 'tokenkey-prod-usage-daily-abort-v1:<legacy_upper_exclusive>'
```

`abort` 只允许在尚未 cutover、且 CHECK 带有 operator ownership 标记时执行；普通查询索引保留。

准备 receipt 会给出绑定上界的 `required_cutover_confirmation`。维护窗口内原样使用该值执行
短 catalog cutover：

```bash
python3 ops/migration/usage_logs_daily_partition.py cutover \
  --prepare-receipt .testing/user-stories/attachments/usage-prepare.json \
  --cutover-receipt .testing/user-stories/attachments/usage-cutover.json \
  --confirm '<required_cutover_confirmation>'
```

锁等待超过 5 秒、上界过期、CHECK 未验证、出现未知 incoming FK、父表仍有全局唯一索引或
切换后 legacy 行数小于 prepare receipt、或父表行数小于 legacy 行数时均 fail closed。账务幂等
仍由 `usage_billing_dedup` 负责。

## 分区维护一次性入口

`data_layer_partition_maintenance.py` 只面向固定的 `us-east-1` / `tokenkey-prod-stage0`，
不接受 target、instance、script 或 command 参数。Prod 已在 Phase4 执行一次（证据：
`.testing/user-stories/attachments/prod-partition-maintenance.json`）；后续仅在
分区覆盖漂移或 cutover 后补洞时使用，每次仍须显式确认串与独立审批。

命令形状固定为：

```bash
python3 ops/migration/data_layer_partition_maintenance.py run \
  --receipt .testing/user-stories/attachments/prod-partition-maintenance.json \
  --confirm tokenkey-prod-partition-maintenance-v1
```

controller 只发送一次固定 SSM 命令，并轮询同一个 `CommandId`。只有远端严格分区维护、覆盖验证和
成功 heartbeat 全部完成后，才会原子创建本地 receipt；已有 receipt 不会被覆盖。该入口不授权删除、
cleanup release、部署、重启或配置修改。
