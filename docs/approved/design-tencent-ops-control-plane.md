---
title: 运维旁路与密钥材料收敛到腾讯云（TAT / COS）
status: pending
approved_by: pending
risk: high
authors: [agent]
created_at: 2026-11-18
revised: 2026-11-18（二次乔布斯复审；待人批）
---

# 运维旁路与密钥材料收敛到腾讯云（TAT / COS）

## 用户决定（2026-11-18）

原话：「运维旁路与密钥材料，也应该改到 腾讯 tat 或者 cos 等，收敛到腾讯云体系内。」

## 乔布斯复审（二次）

> 审阅标准：聚焦、简洁、端到端、设计即工作方式、精品。对一百件好事说不。

**判决：方向仍对；一稿收完后又胖回来了。**

用户原话只有八个字量级的事：**旁路用 TAT，钥匙进腾讯/主机。**  
发版已是 TAT，归档新写已禁 AWS——本设计只是把同一纪律补到「还在摸 AWS 的运维手」。

一稿犯的罪：P0–P3、优先迁清单。  
二稿犯的罪：砍掉清单后又贴回一张十行「消费清单」、再写一遍「决策」复述三闭环、验收与完成定义叠床架屋。  
**清单是实现者打开 `rg configure-aws-credentials` 就能得到的东西，不是设计。**

### 产品（一句话）

**管腾讯机默认只许 TAT；ops 密钥与恢复材料默认只许 `tencent-ops` secret、主机 root-only 文件或腾讯 COS；禁止再新增、并逐步去掉「OIDC→Hybrid SSM 管腾讯机 / SecureString 当活源」。**

### 三个闭环（唯一正文）

1. **禁新增**  
   新代码若用 AWS OIDC+Hybrid SSM 管腾讯机，或把 ops 密钥写入 SecureString → **合不进来**。无「先写 AWS 再迁」开关。

2. **旁路改 TAT**  
   凡对腾讯 prod / cn-prod **下命令**且今日走 SSM 的默认入口，改为既有 `remote_exec` + TAT + `tencent-ops`。  
   不新造执行器；SSM 不准写回 workflow 默认。

3. **密钥离 AWS**  
   活密钥：GitHub `tencent-ops` 或主机 root-only（对齐 `/etc/tokenkey/cos-backup.env`）。  
   离机/恢复：只进腾讯 COS（`design-cos-object-archive.md`）。  
   **停写** SecureString；迁完停读再删。不引入第二套「密钥产品」。

Hybrid 注销 = 默认消费者清零后另批一刀，不是本设计交付物。

### 砍掉的

| 提议 | 判决 |
|---|---|
| 分期史诗 / 设计内消费清单表 | **否** — `rg` 即 backlog |
| 腾讯 Secrets Manager / 自研密钥服务 | **否** |
| 迁 GHCR、清 Oregon、动供应商 AWS | **否** |
| 改拓扑 / 重开蓝绿 | **否** |
| 把 break-glass 也写成产品默认禁 AWS | **否** — 默认零 AWS；应急另批 |
| 未批准就拆 Hybrid / 迁生产钥 | **否** |
| 「TAT 或只读 API」含糊口径 | **否** — 管主机 = TAT；只碰 AWS 资源的 job 不在范围 |

### 完成定义

1. 门禁拦住上述两类新增。  
2. 对腾讯机的**日常**运维默认路径无 `configure-aws-credentials`。  
3. 活 ops 密钥 SSOT 不再是 SecureString；无新写；有一次不读 AWS 的装机/恢复证据后再删旧参数。  
4. 文档残留表与本文一致。

不宣称：Hybrid 已注销、AWS 账号为零、GHCR/Oregon 已迁。

## 现状（前提，不展开）

| 已 TAT / 已禁 AWS 新写 | 仍 SSM 或 SecureString | 不在范围 |
|---|---|---|
| 发版 warm/rolling；对象归档新写；cn-prod 镜像 COS 本体 | probe / Feishu / Caddy / prune / log policy / diagnostics 腾讯机段 / `publish-from-prod` crane；env-secrets 等 SecureString | Oregon、Kiro/Bedrock、只操作 AWS 资源的 job、legacy `deploy-stage0` 应急壳 |

## 验收

门禁红；迁过的入口默认无 OIDC；一次无 SecureString 恢复/装机证据；`deploy/hetzner/README.md` 指针正确。

## 审批门禁

`approved_by: pending`。人改署名并 merge = 批准（`pending` 不得进 `main`）。  
批准前只改文档；批准后按 `rg` 命中逐 PR 抠，禁止大爆炸。
