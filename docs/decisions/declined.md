# 已决定不做

## 2026-11-19 恢复 QA → S3

原话：「QA S3 不需要上传，qa 只往 node1 写归档。」

不做：恢复 QA raw S3 上传，及为此改 Hybrid / 桶策略 / `DenyLightsailEdgeRole`。现行 = 本地 24h + node1（`design-tencent-cos-backup-node1-qa.md`）。

## 2026-11-19 生产 data-layer archive canary

原话：「归档 canary … 应该不需要了吧，有 qa 到 node1，pgpackrest +wal 就行了？」

不做：迁腾讯或续跑冷表 export canary（旧 EC2 / `PROD_CANARY_*`）。库级靠 pgBackRest+WAL；QA 靠 node1。设计已 `archived`：`design-data-layer-prod-export-canary.md`。
