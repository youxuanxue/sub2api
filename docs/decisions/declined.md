# 已决定不做

## 2026-11-19 腾讯整机/卷级快照常备策略

原话：「腾讯整机/卷快照策略，除了 pg 已经有了，其他的备份和快照，也没有什么价值吧？」「直接改。把历史的、不合适的、遗弃的都可以删除了…」

不做：Lighthouse / 卷级定期或常备快照策略（无 EBS/DLM 替代项）。离机保护 = pgBackRest→COS（库）+ 入口 host-config→COS（证书材料）。应用机靠镜像与滚动发版重建；Redis 可新建。Lighthouse 快照随实例销毁、非异地副本，不能当灾备。高危宿主手术前若临时手打快照，用完即丢，不写进策略。

## 2026-11-19 AWS 临时机隔离物理恢复演练

原话（同会话收口）：整机迁移已覆盖真实恢复路径。

不做：另开 AWS 临时机做腾讯 prod 隔离恢复演练，不为该演练申请预算/RestoreRole/OIDC。库恢复走 pgBackRest 迁机（`pgbackrest-host-migration.md` / `tokenkey-host-replacement`）。旧方案文档已删，勿再建。

## 2026-11-19 恢复 QA → S3

原话：「QA S3 不需要上传，qa 只往 node1 写归档。」

不做：恢复 QA raw S3 上传，及为此改 Hybrid / 桶策略 / `DenyLightsailEdgeRole`。现行 = 本地 24h + node1（`design-tencent-cos-backup-node1-qa.md`）。

## 2026-11-19 生产 data-layer archive canary

原话：「归档 canary … 应该不需要了吧，有 qa 到 node1，pgpackrest +wal 就行了？」

不做：迁腾讯或续跑冷表 export canary（旧 EC2 / `PROD_CANARY_*`）。库级靠 pgBackRest+WAL；QA 靠 node1。设计已 `archived`：`design-data-layer-prod-export-canary.md`。
