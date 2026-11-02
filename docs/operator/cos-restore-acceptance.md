# COS 恢复验收

`ops/backup/cos_restore_acceptance.py` 证明一个 COS 仓库**真的能恢复出数据库**，
而不只是能写进去。已批准设计
（`docs/approved/design-tencent-cos-backup-node1-qa.md`）阶段 3 要求这一步，
在它跑通之前，双写只说明对象进去了，不说明数据库能回来。

脚本写出 `host_https_watch` 本来就在找的两份回执
（`repository_verification.json`、`restore_acceptance.json`），成功一次可清除
`repository_verification_missing_or_stale` 与 `restore_acceptance_missing_or_stale`
各 8 天。

## 隔离（prod 与 cn-prod 都在服务真实用户）

- 恢复只在一次性容器里跑；线上数据库容器只被读取
- 不发布端口，恢复实例只监听 unix socket
- 恢复容器**不加入任何 docker 网络** —— 连网关和线上库都按名字也到不了。
  cn-prod 实测：这种容器里 `pg_isready -h tokenkey-postgres` 返回 `no response`
- `--cpus` / `--memory` 上限，加 `nice 19` 与 `ionice -c3`
- pgBackRest 配置只读挂载，且从不使用 `--delta`
- 每轮用独立临时目录，失败也会清理

## 两个必须保持的细节

两条都是在 cn-prod 上踩出来的：

1. **`--target-action=promote`**，不是 `shutdown`。用 `shutdown` 时集群不会走完
   恢复流程，`pg_controldata` 仍读作 `in production`，恢复出的副本根本无法查询。
2. **`max_connections` 不得低于主库**，否则 PostgreSQL 拒绝恢复：
   `recovery aborted because of insufficient parameter settings`。
   脚本从线上库读取该值，不写死常量。

## 用法

先只读确认目标（不恢复任何东西）：

```bash
python3 ops/backup/cos_restore_acceptance.py --repo 2 \
  --config /run/tokenkey-cn-prod-backup/config \
  --host-role tencent-beijing-cn-prod
```

确认后执行并写回执：

```bash
sudo python3 ops/backup/cos_restore_acceptance.py --repo 2 \
  --config /run/tokenkey-cn-prod-backup/config \
  --host-role tencent-beijing-cn-prod --apply
```

prod 的配置目录是 `/run/tokenkey-backup-candidate/config`。prod 库约 21 GB，
演练卷余量需先确认（cn-prod 为 1.6 GB，恢复耗时约 80 秒）。

## 判定

恢复点是**过去**的状态，所以行数和金额可以落后线上，但**不得超过**线上。
`usage_logs` 与 `usage_billing_dedup` 的行数差必须与线上一致 ——
这是区分「一致的恢复」和「写入中途被截断的副本」的关键信号。

## cn-prod 首次验收结果（2026-11-02）

| | 历史点 `20261102-202410F` | 最新点 `...203119I` |
|---|---|---|
| 恢复 | 80.3 秒 | 75.3 秒 |
| 重放+启动 | 2.35 秒 | 2.36 秒 |
| schema | 409 表 / 96 外键 / 281 主键 / 86 序列 | 同上，与线上一致 |
| `usage_logs` | 507,683 | 508,467 |
| 账务对差值 | 26（与线上一致） | 26（与线上一致） |

全程线上容器集合不变、归档 `failed_count` 不增长、网关 HTTP 308。
