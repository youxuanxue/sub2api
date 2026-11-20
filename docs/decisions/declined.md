# 已决定不做

只记人口头否决；现行做法见 [`decided.md`](decided.md) 与 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)。

## 2026-11-20

| 原话（摘要） | 不做 |
|---|---|
| 「美西和北京都用同样的密钥就好。不需要单独加用户。」 | 另建只写 ops-archive 的 CAM 用户；也不再把「cn 补独立 cos-archive.env」当必做项。host-config 与库备份共用现有 backup 钥（见 `decided.md`）。 |

## 2026-11-19

| 原话（摘要） | 不做 |
|---|---|
| 「整机/卷快照…也没有什么价值吧？」「历史的、不合适的、遗弃的都可以删除」 | 腾讯整机/卷**常备**快照（含当 EBS/DLM 替代）。离机只靠 pgBackRest→COS + 入口 host-config→COS；应用/Redis 重建。手术前临时快照用完即丢，不进策略。 |
| 整机迁移已覆盖真实恢复 | AWS 临时机隔离物理恢复演练（预算/RestoreRole/OIDC）。迁机走 `pgbackrest-host-migration` / `tokenkey-host-replacement`；勿再建旧方案文档。 |
| 「QA S3 不需要上传，qa 只往 node1 写归档」 | 恢复 QA→S3 上传，及为此改 Hybrid/桶/`DenyLightsailEdgeRole`。现行 = 本地 24h + node1。 |
| 「归档 canary … 应该不需要了吧，有 qa 到 node1，pgBackRest +wal」 | 生产 data-layer archive canary（旧 EC2 / `PROD_CANARY_*`）。相关设计文档已删除；勿恢复 canary CLI。 |
| 「迁移/交接/历史/approved 应删尽删」（2026-11-20 续） | 恢复 phase1 closeout/activation gates、archive rehearsal、历史 S3 archive-bucket、cn-prod 逻辑 dump 激活文、腾讯运维 closeout 清单，或已删的 ops 回执（SecureString C5 / upstream-merge 审计稿等）。活契约见 `design-cos-object-archive` / `design-tencent-ops-control-plane` / 舰队 README。 |
| 「迁移/交接/历史应删尽删」（2026-11-20 pass3） | 恢复已删的 COS 并行 repo 草案、UCloud Kimi 压测包、relay-watch 排障稿、2026-10-08 multimodal probe ledger/matrix。 |
| 「迁移/交接/历史/approved 应删尽删」（2026-11-20 pass4） | 恢复 UCloud 首发契约 `design-domestic-cluster`、国内 README 迁机/验收回执、rolling-deploy M1–M6 执行表、`docs/deploy/aws-us-openai-gateway-deployment`，或恢复已删除的 `qa-raw-only.md`（删除边界已并入 `design-tencent-cos-backup-node1-qa`）。 |
| 「迁移/交接/历史/approved 应删尽删」（2026-11-20 pass5） | 恢复 `design-prod-entry-multi-app` 的 L1–L8/X1–X4/DNS 执行表，或把 `design-prod-app-parallel-replacement` 迁机/询价/SSM 回执叙事写回全文（双应用与 QA 24h 不变量仍保留）；或恢复 `design-data-plane-split` 停写窗口逐步执行表。 |
