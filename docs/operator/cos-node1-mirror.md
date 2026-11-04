# COS → node1 每日兜底复制

`ops/backup/cos_node1_mirror.py` 是一个只增加数据的、可审计的复制控制器。它用于把已在腾讯 COS 验收完成的数据库备份，按天复制到 node1；QA 原始归档也可使用同一工具，但必须使用独立的 `--namespace qa` 路径。两条路径不会混用，也不会把 QA PostgreSQL 数据库恢复到 node1。

控制器不会创建 COS bucket、修改线上配置、删除 AWS/COS 对象，或删除 node1 文件。凭据不进入仓库；目标目录按 `namespace/snapshot` 分开。

远程 COS 有两条路径。**默认用 `--s3-bucket` 走 S3 API**：COS 本身是 S3 兼容的（pgBackRest 的仓库配置就是 `repo2-type=s3`），node1 已有 boto3，无需安装任何东西。该路径只调用 `list_objects_v2` 和 `get_object`，不执行任何写或删除操作 —— 现有凭据带写权限，所以这一点由 `ops/backup/test_cos_s3_fetch.py` 的测试强制保证。`--source-uri` 走 `rclone` 的旧路径仍保留，但两台生产主机和 node1 都未安装 rclone。

复制流程如下：

1. 先检查 node1 目标文件系统的可用空间：85% 使用率开始告警，90% 使用率禁止新写入，95% 使用率进入紧急状态。S3 路径会自己 list 前缀算出字节数；`rclone` 路径仍需操作员提供 `--expected-bytes`。
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

远程 COS 复制（S3 路径，推荐）。省略 `--apply` 时会 list 前缀并输出 dry-run 计划，含 `object_count` 和 `expected_bytes`：

```bash
# 凭据来自环境：tencent_SecretId / tencent_SecretKey
python3 ops/backup/cos_node1_mirror.py copy \
  --namespace db --snapshot prod-20261103 \
  --s3-bucket tokenkey-prod-backup-usw-1320330536 \
  --s3-prefix prod/postgres \
  --s3-endpoint cos.na-siliconvalley.myqcloud.com \
  --s3-region na-siliconvalley \
  --destination /storage/tokenkey-archive --apply
```

`rclone` 路径（两台主机和 node1 均未安装，保留作兼容）需要显式传入估算值：

```bash
python3 ops/backup/cos_node1_mirror.py copy \
  --namespace db --snapshot 2026-10-31T00-00Z \
  --source-uri tencentcos:prod-cos/postgres/2026-10-31T00-00Z \
  --expected-bytes 1234567890 \
  --destination /srv/node1/archive --bwlimit 20M --apply
```

省略 `--apply` 时只做容量和参数检查并输出 dry-run 计划；不会写入目标目录。`--apply` 也不启用 timer，且仍然不删除任何来源或目标数据。

数据库备份在 node1 上限为 2 TiB，QA raw 上限为 6 TiB；上限按已验证 receipt 的字节数累计，文件系统空间阈值仍是最终保护。该工具不实现 ext4 project quota，避免引入第二套容易漂移的容量 owner。

## 定时同步（node1 上）

`ops/backup/install_node1_db_mirror.py` 在 **node1** 上安装 timer，每 4 小时跑一次
（`00/4:17:00 UTC`，`Nice=19` + `IOSchedulingClass=idle`）。4 小时的依据：COS 保留
7 天全量（`repo2-retention-full=7`），增量每 2 小时落一次，所以不会落后超过一个增量。

```bash
# 在 node1 上，先 plan
python3 install_node1_db_mirror.py install --source <模块目录> \
  --environments prod,cn-prod --expect-hostname "$(hostname)"
# 确认后 apply
python3 install_node1_db_mirror.py install --source <模块目录> \
  --environments prod,cn-prod --expect-hostname "$(hostname)" --apply
```

timer 装在 node1 而非生产主机，因为 2026-11-03 实测：node1 从 COS 并发 16 拉取达
**29.7 MiB/s**，而 prod 主机经跳板推送只有 **0.8 MiB/s**；拉取还完全不占生产主机的
CPU 和出口带宽。并发是关键 —— 单流只有 2.9 MiB/s，prod 的 4,079 个对象里 53% 是
1 MiB 以下的 WAL 段，瓶颈在延迟。

凭据需预先放在 node1 的 `/etc/tokenkey/cos-mirror.env`（600 root），安装器会拒绝
权限过宽的文件，且凭据只经 `EnvironmentFile` 引用，不写入 unit 文件。每天一个
snapshot（`<env>-YYYYMMDD`），重跑会验证已有 receipt 而不重传。

## 不在本文范围

COS bucket 策略、node1 扩盘命令。上线前仍需由人工完成 node1 磁盘/RAID 健康检查、
权限与加密评估。AWS 回退链的退役不属于此工具的权限范围。
