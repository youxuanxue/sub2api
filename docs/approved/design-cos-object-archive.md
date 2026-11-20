---
title: 对象归档统一落腾讯 COS（禁新写 AWS）
status: approved
approved_by: "feng（2026-11-17 会话：乔布斯复审后批准）"
approved_at: 2026-11-17
risk: high
authors: [agent]
created_at: 2026-11-17
revised: 2026-11-20（压缩复审散文；目的地与禁写契约保留）
---

# 对象归档统一落腾讯 COS（禁新写 AWS）

## 用户决定（2026-11-17）

原话：「核心是『以后凡是对象归档都别再落 AWS』。」确认按此推进。

## 纪律（三个闭环）

1. **禁写**：归档写入解析到 AWS S3 / 已知 AWS archive 桶 → **立刻失败**；无「先写 AWS 再迁」。
2. **落盘**：host-config / 批准的 break-glass 只写 **一个** 腾讯 COS 桶（prod 美西）；PUT→HEAD→receipt；URI=`s3://<cos-bucket>/<key>` + identity endpoint（与 pgBackRest COS 一致）。
3. **旧桶收尸**：AWS `tokenkey-prod-archive-*` 30 天自删；**不**搬历史对象到 COS。

不做：复活冷表→对象存储流水线、cn 对称 ops-archive 桶、ARCHIVE/Glacier 模仿、独立 staging 桶、历史对象搬迁、把镜像分发当归档、改确认串后缀、搅 pgBackRest/QA、未批准建桶。

完成：写入路径对 AWS fail-closed；COS 有唯一 ops-archive 桶 + 身份；host-config 有文档化命令与 receipt；break-glass 同 identity 或同样禁写 AWS。不宣称冷表自动归档或历史对象已迁。

## 现状（决策前提）

| 通路 | 现状 | 本设计动作 |
|---|---|---|
| 数据库 pgBackRest | 已在腾讯 COS | **不改**；[design-tencent-cos-backup-node1-qa.md](design-tencent-cos-backup-node1-qa.md) |
| QA raw | 本地 24h + node1 | **不改** |
| ops 长期 archive | `frozen`；AWS 桶 30 天自删 | 禁写 AWS；新写只进下方唯一 COS 桶 |
| host-config 等离机包 | 曾写 AWS archive | 改同一 COS 桶 |

历史 AWS S3 archive-bucket / promote 设计文已删除；**存储目的地与证据语义**均以本文为准
（`archive_mode: frozen`；新写 COS only）。

## 决策（收敛后）

1. 新对象归档 **只允许** 腾讯 COS；AWS 为归档目的地时失败关闭。
2. **一个** prod 美西 COS 桶承载：长期证据前缀 + export 周转前缀 + host-config 前缀。不与 pgBackRest 备份桶共用。
3. 保留策略（可在实现前微调数字，不可加回 ARCHIVE 层）：  
   - `prod/ops-archive/`：**400 天**过期删除（Standard）  
   - `prod/ops-archive-export/`：**7 天**过期  
   - `prod/host-config/`：**90 天**过期  
4. 证据语义不变：`deletion_authorized=false`；promote 不授权 DROP；确认串保持现网 `*-v1`。
5. `archive_mode` 保持 `frozen`，除非另文批准。

## 资源与 owner（批准后）

| 项 | 约定 |
|---|---|
| 桶名 | **已建** `tokenkey-prod-ops-archive-1320330536`（写入 `ops/archive/cos-archive.json`） |
| 地域 / endpoint | 美西 `na-siliconvalley`；CLI 用裸 host `https://cos.na-siliconvalley.myqcloud.com` + `addressing_style=virtual`（由 `cos_archive_identity.aws_cli_env` 强制） |
| lifecycle | `ops-archive` 400d / `ops-archive-export` 7d / `host-config` 90d（已 apply） |
| 身份 | `ops/archive/cos_archive_identity.py` + `TK_COS_ARCHIVE_CREDENTIALS` 或数据机 `/etc/tokenkey/cos-backup.env`（与 pgBackRest 同钥；禁止进 Git） |
| fail-closed | promote / export / host-config 共用「拒绝 AWS」 |
| promote / export | 已接到同一 COS identity |
| host-config | `ops/archive/upload_host_config.py` |
| 文档 | `ops/archive/README.md`、`deploy/hetzner/README.md` 残留表 |

## 明确不做

见上文「砍掉的」表。另：不改 Oregon / Hybrid / OIDC；空 AWS archive 桶删除另批。

## 验收

1. 单测：identity、AWS 目的地拒绝、plan 无写。  
2. 合成：对真实 COS（或批准的 mock）PUT → HEAD → GET。  
3. host-config 或合成 batch 走通一次并留 receipt。  
4. ~~frozen 下 `data_layer_archive_health`~~（2026-11-19 已删；daily safety 对
   ARCHIVESTATS 置 `applicable=false`）。  
5. 文档与残留表一致。

## 审批门禁

- 设计已于 2026-11-17 批准。  
- 建桶、第一条真实对象、CLI 切默认，仍各需会话确认一步（本会话「批准」覆盖设计；实现按闭环推进时逐步确认不可逆动作）。
