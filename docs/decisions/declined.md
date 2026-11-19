# 已决定不做

## 2026-11-19 恢复 QA raw S3 上传

原话：「QA S3 不需要上传，qa 只往 node1 写归档。」

不做：恢复 QA → S3 写入身份、为 Hybrid 补 `TencentUswPhysicalBackup`、为 QA 上传改桶策略 / `DenyLightsailEdgeRole`、恢复 `tokenkey-qa-maintenance` S3 路径。现行路径见 `docs/approved/design-tencent-cos-backup-node1-qa.md`（本地 24h + node1）与 `ops/qa/README.md`。

## 2026-11-19 迁腾讯 / 续跑 data-layer 生产 archive canary

原话：「归档 canary 是什么？还需要吗？应该不需要了吧，有 qa 到 node1，pgpackrest +wal 就行了？」

不做：把 `ops/archive/data_layer_archive_prod_canary.py`（`PROD_CANARY_CONTAINER` / 旧 EC2+CFN）迁到腾讯或恢复为现网必做演练。 <!-- script-ref-allow-missing: retired 2026-11-19 -->
该 canary 是**业务库冷表 export→独立库验回**的试验通道，不是 QA 轨迹归档，也不是 pgBackRest 库级恢复。库级保护继续用 COS pgBackRest + WAL + `cos_restore_acceptance`；QA 用 node1。

落地：同日删除 prod canary / export / promote / closeout / cleanup_hold / retention_activation / `data_layer_archive_health` 及对应测试；daily diagnostics 对 `ARCHIVESTATS` 置 `applicable=false`。设计文档 `design-data-layer-prod-export-canary.md` → `archived`。
