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

## 尚需独立验收的资源

- node1 东京临时 reader、克隆盘、快照和入口：导出任务完成且验证成功后再回收；
  两块克隆数据盘不会随 EC2 自动删除。不能把最后一条成功 SSM 命令当作完整任务验收。
- 新加坡其它项目机器：先核对归属，受保护的生产资源保留。
- 历史快照：按来源和数据覆盖消重，保留最终恢复点与明确保护的快照。
- 共享 SSM/IAM、S3 桶与旧路径名：先核对现网及历史恢复依赖，不能随计算节点批量删除。

## 仓库与运行态

`retired=true` 是已删除身份的拒绝标记；`deployable=false` 单独只表示非现役。
`--allow-planned` 不覆盖退役，新建须使用新身份。旧 prod 的 `aws` 模式直接拒绝，
现网仍由 `auto` / `aws-fresh` 解析。

QA 上传暂停由 `fresh-migration-20261015.json` 中当前 prod 的 `paused_jobs` 持有。
宿主监控会检查暂停任务保持停用，意外恢复则报警；不暂停 capture、boundary 或 pgdump。
修改仓库不会安装 watcher、启动任务或删除资源；宿主脚本安装仍需独立验证与相应授权。
