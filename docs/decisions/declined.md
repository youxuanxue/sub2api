# 已决定不做

只记人口头否决；现行做法见 [`decided.md`](decided.md) 与 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)。

## 2026-11-19

| 原话（摘要） | 不做 |
|---|---|
| 「整机/卷快照…也没有什么价值吧？」「历史的、不合适的、遗弃的都可以删除」 | 腾讯整机/卷**常备**快照（含当 EBS/DLM 替代）。离机只靠 pgBackRest→COS + 入口 host-config→COS；应用/Redis 重建。手术前临时快照用完即丢，不进策略。 |
| 整机迁移已覆盖真实恢复 | AWS 临时机隔离物理恢复演练（预算/RestoreRole/OIDC）。迁机走 `pgbackrest-host-migration` / `tokenkey-host-replacement`；勿再建旧方案文档。 |
| 「QA S3 不需要上传，qa 只往 node1 写归档」 | 恢复 QA→S3 上传，及为此改 Hybrid/桶/`DenyLightsailEdgeRole`。现行 = 本地 24h + node1。 |
| 「归档 canary … 应该不需要了吧，有 qa 到 node1，pgBackRest +wal」 | 生产 data-layer archive canary（旧 EC2 / `PROD_CANARY_*`）。设计 `design-data-layer-prod-export-canary.md` 已 `archived`。 |
