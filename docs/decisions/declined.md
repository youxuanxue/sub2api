# 已决定不做

只记人口头否决；现行做法见 [`decided.md`](decided.md) 与 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)。
已删文档清单的 owner 是 [`docs/approved/README.md`](../approved/README.md)「已删除的历史设计」，本文件不逐 pass 记账。

## 2026-11-22

| 原话（摘要） | 不做 |
|---|---|
| 「直接用主账号即可 /etc/tokenkey/cos-backup.env 。不需要单独创建子账户。」 | 为 Managed Batch 另建 CAM 子用户或独立 API 密钥（复用数据机 `cos-backup.env`，见 `decided.md`）。 |

## 2026-11-20

| 原话（摘要） | 不做 |
|---|---|
| 「美西和北京都用同样的密钥就好。不需要单独加用户。」 | 另建只写 ops-archive 的 CAM 用户；也不再把「cn 补独立 cos-archive.env」当必做项。host-config 与库备份共用现有 backup 钥（见 `decided.md`）。 |
| 「迁移/交接/历史/approved 应删尽删」 | 恢复已删的迁机/交接/压测/tombstone/一次性 orphan 工具、已交付审批锚点、已执行容量审批回执，或独立 `pgdump-retirement` 摘要（已并入 `physical-backup`）。活契约见舰队 README 与 approved 活设计；已删边界见 approved README「已删除的历史设计」。 |

## 2026-11-19

| 原话（摘要） | 不做 |
|---|---|
| 「整机/卷快照…也没有什么价值吧？」「历史的、不合适的、遗弃的都可以删除」 | 腾讯整机/卷**常备**快照（含当 EBS/DLM 替代）。离机只靠 pgBackRest→COS + 入口 host-config→COS；应用/Redis 重建。手术前临时快照用完即丢，不进策略。 |
| 整机迁移已覆盖真实恢复 | AWS 临时机隔离物理恢复演练（预算/RestoreRole/OIDC）。迁机走 `pgbackrest-host-migration` / `tokenkey-host-replacement`；勿再建旧方案文档。 |
| 「QA S3 不需要上传，qa 只往 node1 写归档」 | 恢复 QA→S3 上传，及为此改 Hybrid/桶/`DenyLightsailEdgeRole`。现行 = 本地 24h + node1。 |
| 「归档 canary … 应该不需要了吧，有 qa 到 node1，pgBackRest +wal」 | 生产 data-layer archive canary（旧 EC2 / `PROD_CANARY_*`）。相关设计文档已删除；勿恢复 canary CLI。 |

## 2026-11-21

| 原话（摘要） | 不做 |
|---|---|
| 「分子排除 users.role=admin。不应该排除这个吧。而是去修复错误探针和不存在的模型？」 | 从「真实用户体验受损 / 客户端失败增多」分子剔除 admin（#3382）。admin 运维流量仍算可观测信号；应停错误客户端/探针、撤已下架模型入口。 |
