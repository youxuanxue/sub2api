---
title: 对象归档统一落腾讯 COS（禁新写 AWS）
status: approved
approved_by: "feng（2026-11-17 会话：乔布斯复审后批准）"
approved_at: 2026-11-17
risk: high
authors: [agent]
created_at: 2026-11-17
revised: 2026-11-17（乔布斯复审收敛；同日批准）
supersedes_destination: design-prod-archive-bucket.md
---

# 对象归档统一落腾讯 COS（禁新写 AWS）

## 用户决定（2026-11-17）

原话：「核心是『以后凡是对象归档都别再落 AWS』。」确认按此推进。

## 乔布斯复审

> 审阅标准：聚焦、简洁、端到端、设计即工作方式、精品。对一百件好事说不。

**判决：方向对，原稿过重。**  
用户要的不是「再造一条冷表归档产品」，而是一句能执行的纪律：**新的归档对象不准再落到 AWS。**  
现网 ops archive 已是 `frozen`，日常几乎不写。若按旧 S3 契约原样搬（staging + promote + Glacier/ARCHIVE + 400 天 + cn 对称桶），是在给几乎不发生的 break-glass 造博物馆。

### 留下的（三个闭环）

1. **禁写闭环（主产品）**  
   所有「对象归档写入」入口解析目的地时：落到 AWS S3 / 已知 AWS archive 桶名 → **立刻失败**。  
   没有例外开关「先写 AWS 再迁」。这是用户原话的牙齿。

2. **落盘闭环（唯一活桶）**  
   真要归档时（host-config 离机包、批准的 break-glass 证据），只写入 **一个** 腾讯 COS 桶（prod 美西，与备份同区便于运维）。  
   契约极短：PUT → HEAD 校验大小/校验和 → 本地 receipt。  
   URI 形态锁定：`s3://<cos-bucket>/<key>` + identity 里的 endpoint（与 pgBackRest COS 一致），不发明第二套 `cos://`。

3. **旧桶收尸闭环（已在跑，不耦合）**  
   AWS `tokenkey-prod-archive-*` 已 30 天自删。空了再另批删桶。  
   **不**搬历史对象到 COS。收尸成功不依赖 COS 桶是否已建。

### 砍掉的

| 提议 | 判决 | 理由 |
|---|---|---|
| 复活日常冷表→对象存储流水线 | **否** | `archive_mode: frozen`；热层 PG retention 已是稳态 |
| cn-prod 对称 ops-archive 桶 | **否** | 无当前写入需求；要时再开一行变更 |
| 90 天转 COS ARCHIVE / Glacier 模仿 | **否** | 取回慢、费用坑、无人演练恢复；先 Standard + 过期删除 |
| 独立 staging 桶 | **否** | 同桶前缀 `…/export/` 即可；一年用几次不配第二桶 |
| 历史 AWS archive 搬迁 | **否** | 成本/价值差；需要时另批 |
| 镜像分发桶当「归档」迁走 | **否** | 那是发版，不是 archive |
| 确认串改成 `-cos` 后缀 | **否** | 保留现有 `tokenkey-prod-archive-*-v1`，只换后端 |
| 本设计改 pgBackRest / QA | **否** | 已有 SSOT，禁止搅进来 |
| 未批准就建桶、改生产默认 | **否** | 高风险门禁 |

### 完成定义

同时满足才算「禁写 AWS」落地：

1. 仓库里归档写入路径对 AWS 目的地 **fail-closed**（单测锁死）。
2. COS 上存在 **一个** ops-archive 桶 + root-only 身份文件；合成对象 PUT/HEAD/GET 通过。
3. host-config（或等价一次性离机包）有一条文档化命令写到该桶，并留下 receipt。
4. break-glass promote/export **要么**已接到同一 identity，**要么**在接到之前对 AWS 同样 fail-closed（不允许半截「还能写 S3」）。
5. 文档写明：新对象归档 = COS only；AWS archive = 收尸中。

不宣称：冷表自动归档已恢复、跨地域双归档、ARCHIVE 存储类、历史对象已迁移。

## 现状（决策前提）

| 通路 | 现状 | 本设计动作 |
|---|---|---|
| 数据库 pgBackRest | 已在腾讯 COS | **不改**；[design-tencent-cos-backup-node1-qa.md](design-tencent-cos-backup-node1-qa.md) |
| QA raw | 本地 24h + node1 | **不改** |
| ops 长期 archive | `frozen`；AWS 桶 30 天自删 | 禁写 AWS；新写只进下方唯一 COS 桶 |
| host-config 等离机包 | 曾写 AWS archive | 改同一 COS 桶 |

[design-prod-archive-bucket.md](design-prod-archive-bucket.md) 保留历史 promote **证据语义**；**存储目的地**以本文为准。

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
