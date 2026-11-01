# COS → node1 每日兜底复制（草案）

`ops/backup/cos_node1_mirror.py` 是一个只增加数据的、可审计的复制控制器。它用于把已在腾讯 COS 验收完成的数据库备份，按天复制到 node1；QA 原始归档也可使用同一工具，但必须使用独立的 `--namespace qa` 路径。两条路径不会混用，也不会把 QA PostgreSQL 数据库恢复到 node1。

控制器不会创建 COS bucket、修改线上配置、启动 timer、删除 AWS/COS 对象，或删除 node1 文件。远程 COS 通过运维机已有的 `rclone` 配置访问，凭据不进入仓库；复制使用限速、少量并发和重试上限。来源目录采用 `--immutable`，目标目录按 `namespace/snapshot` 分开。

复制流程如下：

1. 先检查 node1 目标文件系统的可用空间，默认至少保留 20% 空闲，并按源大小的 1.25 倍预留空间。远程 COS 必须由操作员提供只读测量得到的 `--expected-bytes`。
2. 写入随机命名的 `.staging` 目录；同一 snapshot 已存在时只接受已有且可验证的 receipt，不覆盖它。
3. 对每个文件计算 SHA-256、字节数和路径，写入 `_manifest.json`，再完整校验 manifest。
4. 写入 `_receipt.json` 后，以目录原子重命名发布。只有发布后的目录才是可消费的快照。
5. 验收或巡检可运行 `verify`，重新计算所有文件哈希。校验失败时保留现场，不删除源数据。

示例（本地 rehearsal，不触碰线上）：

```bash
python3 ops/backup/cos_node1_mirror.py copy \
  --namespace db --snapshot 2026-10-31T00-00Z \
  --source /tmp/cos-export \
  --destination /srv/node1/archive \
  --bwlimit 20M --transfers 2 --checkers 4 --apply

python3 ops/backup/cos_node1_mirror.py verify \
  --snapshot /srv/node1/archive/db/2026-10-31T00-00Z
```

远程 COS 复制必须先通过 COS 清单或只读 `rclone size` 得到快照大小，再显式传入估算值：

```bash
python3 ops/backup/cos_node1_mirror.py copy \
  --namespace db --snapshot 2026-10-31T00-00Z \
  --source-uri tencentcos:prod-cos/postgres/2026-10-31T00-00Z \
  --expected-bytes 1234567890 \
  --destination /srv/node1/archive --bwlimit 20M --apply
```

省略 `--apply` 时只做容量和参数检查并输出 dry-run 计划；不会写入目标目录。`--apply` 也不启用 timer，且仍然不删除任何来源或目标数据。

该草案不包含 systemd timer、cron、COS bucket 策略或 node1 扩盘命令。上线前仍需由人工完成 node1 数据盘扩容、磁盘/RAID 健康检查、权限与加密评估，然后在低峰先做一次小快照 rehearsal，再由独立验收确认 receipt、容量、带宽和告警行为。AWS 回退链的退役不属于此工具的权限范围。
