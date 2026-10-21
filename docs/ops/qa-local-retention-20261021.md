# QA 本地 24 小时保留策略验收（2026-10-21）

当前 prod 身份以 [现网入口](../../deploy/hetzner/README.md) 为准。用户已明确授权未归档 QA 数据保留 24 小时后删除；[决策与边界](../approved/qa-raw-only.md) 为执行依据。

北京时间 11:26 验收通过：

- `tokenkey-qa-provision.timer` 与 `tokenkey-qa-cleanup.timer` 均 enabled/active，执行成功；前者每小时补齐未来 72 小时，后者每 5 分钟清理完整过期小时，重启后补跑。
- 分三批清理 99 个过期分区及 72 个过期小时的原始文件目录。最终只读计划：过期分区 0、过期文件小时 0、待处理小时 0。未归档删除保留真实 source-unavailable 回执，未标为归档成功。
- QA 原始文件由约 20 GiB 降至 4.6 GiB；QA 分区由约 972 MiB 降至 96 MiB。数据盘使用率 42% → 31%，可用空间约 81 GiB → 97 GiB。
- 未来分区 72/72；最近两分钟 QA 写入 1047 条；等待锁 0；WAL archive failed_count 0。网关、PG、Redis 健康。
- S3 归档和旧 boundary timer 仍 disabled/inactive；旧 DROP pause 保持原值。历史 capture unresolved_failures 保留，不宣称历史捕获完整性已修复。
- 新清理任务纳入现有宿主 watcher，provision/cleanup 均无失败；安装 dry-run 通知数 0。

## 工件与回执

- 安装目录：`/usr/local/lib/tokenkey/qa-local/b125c568906dcf83`
- 二进制 SHA-256：`f8e6f5893f9939b86a2aa830f86f051d1d5b0ed6847423d0a63dccc394f5e362`
- 原 unit/定时器备份：`/var/backups/tokenkey-qa-local/20261021T032354125539Z`
- 最近执行回执：`/var/lib/tokenkey/qa-local/provision.json`、`cleanup.json`；只读计划单独写 `cleanup-plan.json`。
- 部署 SSM：`355d50d9-b67d-40ae-8996-094fd9f87987`；最终验收：`be301a3e-ce92-4647-9ba8-12b9bf11aea8`；watcher 更新：`977f3990-b88c-47c8-bc0a-4c7383b14c46`。
- 本次执行端原始日志：`/tmp/tokenkey-health-audit/qa-cleanup-*.txt`。

停止后续删除可停止并禁用 `tokenkey-qa-cleanup.timer`，等待当前 service 完成；回滚 unit 不能恢复已删除且未归档的数据。恢复 S3 上传需另行核对生命周期 owner 与源数据，不启用旧 boundary 来替代本地任务。

## 验证

真实 PostgreSQL 18 测试覆盖：24 小时边界、只读计划、未归档删除、配对文件清理、其它数据保留、锁冲突拒删、软链接/DEFAULT 分区保护、源已删除后的续做、每轮 48 小时上限与续批。宿主测试覆盖目录挂载边界、错误回执、旧 owner/摘要保护和安装失败回退；既有 Go lifecycle 测试与 watcher 测试通过。最终 full preflight PASS（185.7 秒），审查无阻塞项。
