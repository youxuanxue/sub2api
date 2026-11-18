---
title: 运维旁路与密钥材料收敛到腾讯云（TAT / COS）
status: pending
approved_by: pending
risk: high
authors: [agent]
created_at: 2026-11-18
revised: 2026-11-18（乔布斯复审收敛；待人批）
---

# 运维旁路与密钥材料收敛到腾讯云（TAT / COS）

## 用户决定（2026-11-18）

原话：「运维旁路与密钥材料，也应该改到 腾讯 tat 或者 cos 等，收敛到腾讯云体系内。」

## 乔布斯复审

> 审阅标准：聚焦、简洁、端到端、设计即工作方式、精品。对一百件好事说不。

**判决：方向对，原稿过重。**  
用户要的不是「再写一份四期 AWS 退役计划」，而是把已经做完一半的事说完：  
**管腾讯机的手，只许用腾讯的手；钥和恢复材料，只许落腾讯或主机，不许再进 AWS Parameter Store。**

发版 warm/rolling 已经是 TAT。对象归档新写已经禁 AWS。  
剩下的不是新平台，是把还在用 Hybrid SSM / OIDC / SecureString 的**旁路和钥匙**抠干净。  
原稿的 P0–P3、五条优先迁清单、双读窗口散文，是在给清理工作办展览。

### 留下的（三个闭环）

1. **禁新增闭环（牙齿）**  
   新 workflow / 新脚本若用 AWS OIDC + Hybrid SSM 去管腾讯机，或把 ops 密钥写入 AWS SecureString → **拒绝合入**（preflight / sentinel）。  
   没有「先写 AWS 再迁」的例外开关。

2. **远程执行闭环（主产品）**  
   今日凡是「对腾讯 prod / cn-prod 主机下命令」且仍走 SSM 的入口，改为既有 `remote_exec` + TAT + `tencent-ops`。  
   包括但不限于：probe、Feishu sync、Caddy sync、GHCR prune、log policy、prod 上 crane 导出。  
   不做第二套执行器；SSM 从**默认路径删除**，不写回 workflow 默认。

3. **密钥闭环（同一纪律）**  
   - 活密钥：GitHub `tencent-ops` secrets，或主机 root-only 文件（对齐 `/etc/tokenkey/cos-backup.env` 模式），由 TAT 安装脚本写入。  
   - 离机/恢复材料：只进腾讯 COS（对齐 `design-cos-object-archive.md`）。  
   - **禁止新写** AWS SecureString；旧参数只读到迁完，再删。  
   不引入腾讯「密钥管理产品」第二套名词。

Hybrid 注销不是第四个产品：当上面两条没有默认消费者时，**另批一刀**拆掉。不配单独史诗。

### 砍掉的

| 提议 | 判决 | 理由 |
|---|---|---|
| 四期 P0–P3 项目制正文 | **否** | 三闭环 + 完成定义够用；实现 PR 按入口逐个勾 |
| 设计里枚举五条「优先迁」当契约 | **否** | 那是 backlog；契约是「SSM 管腾讯机」清零 |
| 新建 Tencent Secrets Manager / 自研密钥服务 | **否** | 已有 GH secret + 主机文件 + COS |
| 把 GHCR 镜像源迁走 | **否** | 用户说的是旁路与密钥，不是换 registry |
| Oregon / Kiro / Bedrock 当残留清掉 | **否** | 出口与供应商协议，不是控制面 |
| 改入口拓扑 / 重开蓝绿 | **否** | 禁止搅进来 |
| 「应急也禁止 AWS」写进产品默认 | **否** | 默认路径零 AWS；break-glass 另批，不写回默认 |
| 未批准就删 Hybrid / 迁生产 SecureString | **否** | 高风险门禁 |
| 为双读窗口写长流程专章 | **否** | 实现时「先加新源、再停旧读、再删」一行纪律即可 |

### 完成定义

同时满足才算「收敛到腾讯云体系」落地：

1. 仓库门禁：新增「OIDC→SSM 管腾讯机」或「ops 密钥写入 SecureString」**合不进来**。  
2. 对腾讯机的日常运维入口（probe / sync / prune / 镜像导出等）**默认零** `configure-aws-credentials`；脚本默认通道是 TAT。  
3. 活 ops 密钥与恢复材料的 SSOT 是 `tencent-ops` secret、主机 root-only 文件或 COS；文档与代码不再把 SecureString 当活源。  
4. SecureString **无新写**；旧参数删除前有一次「不读 AWS 的装机/恢复」证据。  
5. Hybrid / OIDC 若仍存在，只服务未迁完的遗留消费者或显式 break-glass——**不是**日常默认。

不宣称：已注销全部 Hybrid、已零 AWS 账号、GHCR 已迁、Oregon 已下线。

## 现状（决策前提）

| 通路 | 现状 | 本设计动作 |
|---|---|---|
| 发版 warm / rolling | 已 TAT | **不改** |
| 对象归档新写 | 已禁 AWS → COS | **不改**；密钥离机复用同一纪律 |
| cn-prod 镜像 COS | 已；`publish-from-prod` 仍 OIDC→SSM 跑 crane | 该导出改 TAT |
| probe / Feishu / Caddy / prune / 部分 diagnostics | 仍 Hybrid SSM | 改 TAT |
| GHCR PAT、env-secrets、pgdump 恢复等 | SecureString | 停写；活路径迁 GH secret / 主机文件 / COS |
| Oregon、供应商 AWS | 必须留 | **不改** |

## 决策（收敛后）

1. **管腾讯机 = TAT**（`remote_exec` + `tencent-ops`）。SSM 不是默认运维通道。  
2. **ops 密钥 = 腾讯侧或主机**，禁止新写 AWS SecureString / 把 AWS 当活 SSOT。  
3. **禁新增**：OIDC→SSM 管腾讯机、SecureString 存 ops 密钥，门禁拒绝。  
4. Hybrid 拆除是消费者清零后的收尸，不是本期必交付物。

## 明确不做

见上文「砍掉的」表。

## 验收

1. 门禁单测或 sentinel：非法新增路径失败。  
2. 迁完的每个入口：workflow 无 AWS OIDC 默认步骤；手工/CI 跑通。  
3. 密钥：一次不读 SecureString 的恢复或装机演练留证据。  
4. `deploy/hetzner/README.md` 残留表与本文一致。

## 消费清单（实现勾选，非契约）

合入门禁看「完成定义」，下表只供逐 PR 勾选：

| 入口 | 今日依赖 | 目标 |
|---|---|---|
| `distribute-cn-image` `publish-from-prod` | OIDC→SSM 跑 crane | TAT |
| `prod-feishu-config-sync` / `repair` | OIDC→SSM | TAT |
| `ops-stage0-ghcr-prune-timer` | OIDC→SSM | TAT |
| `ops-stage0-container-log-policy` | OIDC→SSM | TAT |
| `ops-daily-diagnostics`（腾讯机段） | OIDC→SSM | TAT / 既有扇出 |
| `ops-payment-billing-watch` / `client-fidelity-watch` prod 段 | OIDC→SSM | TAT 或只读 API |
| Caddy sync `*_via_ssm` | SSM | TAT |
| `backup-env-secrets-via-ssm` / SecureString 恢复材料 | Parameter Store | 主机文件 + COS；停写 SecureString |
| `deploy-stage0` / pgdump canary / physical-restore | OIDC→SSM legacy | 保持失败关闭或显式应急，不挡收敛 |
| `pricing-registry-publish` / `sync-docs-to-pages` | OIDC | 若只碰 AWS 资源则**不在**本设计范围 |

## 审批门禁

`approved_by: pending`。人把 `approved_by` 改成自己并 merge = 批准。  
`pending` 不得进 `main`。批准前只许改文档/决策台账；**禁止**删 Hybrid、迁生产密钥、改默认通道。  
批准后按上表逐 PR 推进，不捆绑大爆炸。
