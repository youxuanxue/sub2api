# AWS / Hcloud 退役后的运维边界

2026-10-17 的 AWS API 复核确认：美东、伦敦、俄勒冈、俄亥俄已无旧 TokenKey 计算实例、
EBS 卷或公网静态 IP。旧 Hcloud 主机也已退役。当前服务身份与日本代理只见
[舰队清单](../../deploy/hetzner/README.md)，旧切流回执描述的是当时状态。

## 历史数据回查

旧 AWS 节点冻结整库、私有运行恢复包、清单及校验回执：

`s3://tokenkey-prod-archive-682751977094/prod/legacy-node-archive/20261017-aws-retirement/`

其中整库已执行下载 SHA 校验、隔离恢复、表计数与 usage 聚合核对。
两台清理前已停止的旧 London 机器以最终 Lightsail 原生快照保留，不宣称已做 SQL 恢复。
该归档前缀不受另一个 ops-archive 前缀的 400 天规则覆盖；按用户要求保留，日后自行决定清理。
恢复包可能含凭据，不能输出或放入 Git；运营回查仅恢复到独立库。

旧 Hcloud edge 的归档入口见 [原退役验收](../approved/aws-migration-closeout-20261016.md)。
历史 QA raw 不在本次搬迁范围；已存在的原始回执保持不变。

## node1 临时 reader 回收完成

2026-10-17 14:33 UTC，按用户批准的最小核查路径完成回收，
[云资源终态回执](tokyo-reader-cleanup-20261017.json)确认临时 reader 已 terminated，
两块克隆盘与根盘均已删除，共释放 330 GiB；原始快照仍为 completed 并保留。

两份整库导出与两份用户每日用量清单保留在归档桶的
`prod/node1-precutover/20261017/tokyo-current/` 和 `tokyo-snapshot/`。
文件大小、本地 MD5 与单段 SSE-S3 ETag 一致，另保存 SHA-256；
详细导出校验与清理回执在同批 `reader-cleanup-verification/` 前缀。
未执行完整 SQL 恢复或 EBS 灾备演练，不将文件校验表述为完整恢复验收。
生产网关、Caddy、PostgreSQL、Redis 的容器身份与启动时间未变。
此项已关闭，不再新建 reader 或重复执行删除；快照和导出继续保留。

## 保留资源与后续边界

- 新加坡其它项目机器：先核对归属，受保护的生产资源保留。
- 历史快照：按来源和数据覆盖消重，保留最终恢复点与明确保护的快照。
- 共享 SSM/IAM、S3 桶与旧路径名：先核对现网及历史恢复依赖，不能随计算节点批量删除。

## 仓库与运行态

`retired=true` 是已删除身份的拒绝标记；`deployable=false` 单独只表示非现役。
`--allow-planned` 不覆盖退役，新建须使用新身份。旧 prod 的 `aws` 模式直接拒绝，
现网由 `auto` → 腾讯美西解析（矩阵 `deployable=true`）。`aws-fresh` 仅冻结 Tokyo 保留身份，默认拒绝。

QA 上传暂停由 `fresh-migration-20261015.json` 中当前 prod 的 `paused_jobs` 持有。
宿主监控会检查暂停任务保持停用，意外恢复则报警；capture 与 pgBackRest 物理备份继续运行（旧 pgdump 已退役）。
boundary 因独立的历史归档验收问题保持停用，DROP pause 保留；不能因暂停上传而掩盖该问题。
暂停期巡检与当前验收见 [QA 运维入口](../../ops/qa/README.md#暂停期间的巡检)。
修改仓库不会安装 watcher、启动任务或删除资源；宿主脚本安装仍需独立验证与相应授权。
