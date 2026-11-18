---
title: 运维旁路与密钥材料收敛到腾讯云（TAT / COS）
status: pending
approved_by: pending
risk: high
authors: [agent]
created_at: 2026-11-18
revised: 2026-11-18（三次乔布斯复审；待人批）
---

# 运维旁路与密钥材料收敛到腾讯云（TAT / COS）

## 用户决定（2026-11-18）

原话：「运维旁路与密钥材料，也应该改到 腾讯 tat 或者 cos 等，收敛到腾讯云体系内。」

## 乔布斯复审

> 审阅标准：聚焦、简洁、端到端、设计即工作方式、精品。对一百件好事说不。

**判决：方向对。再砍——产品不是「迁移项目」，是两条纪律。**

发版已是 TAT，归档新写已禁 AWS。用户要的是同一句话补完：  
**还在用 AWS 摸腾讯机、还把钥匙放 Parameter Store 的手，停。**

不是新平台，不是分期史诗，不是消费清单（那是 `rg`）。  
不是「逐步」：日常默认要么已是腾讯侧，要么还没做完——完成定义说清楚。

### 两条纪律

1. **管腾讯机 = TAT**（既有 `remote_exec` + `tencent-ops`）。  
   对 prod / cn-prod 下命令的**日常默认**不得再走 Hybrid SSM / OIDC。不新造执行器。

2. **ops 密钥 = `tencent-ops` secret、主机 root-only、或腾讯 COS**。  
   禁止把 SecureString 当活源；**停写**；迁完停读再删。不发明第二套密钥产品。离机材料对齐 `design-cos-object-archive.md`。

### 一道门牙

新代码若违反上两条（OIDC→SSM 管腾讯机，或 ops 钥写入 SecureString）→ **合不进来**。  
无「先写 AWS 再迁」开关。

存量：按 `rg` 命中逐 PR 抠到日常默认合规。Hybrid 注销是默认消费者清零后另批，不是本文交付物。

### 砍掉的

| 提议 | 判决 |
|---|---|
| 迁移分期 / 设计内入口清单 | **否** — `rg` |
| 密钥新产品（含云厂商 Secrets 套件当本设计必选项） | **否** |
| 迁 GHCR、清 Oregon、动供应商 AWS | **否** |
| 改拓扑 / 重开蓝绿 | **否** |
| break-glass 写进产品默认 | **否** — 默认零 AWS；应急另批 |
| 未批准就拆生产 | **否** |
| 把「禁新增」写成与纪律并列的第三套产品故事 | **否** — 门牙，不是产品 |

### 完成定义

1. 门牙合入：违例新增合不进来。  
2. 对腾讯机的**日常**运维默认路径无 `configure-aws-credentials`。  
3. 活 ops 密钥 SSOT 不是 SecureString；无新写；有一次不读 AWS 的装机/恢复证据后删旧参数。  
4. `deploy/hetzner/README.md` 残留表述与本文一致。

不宣称：Hybrid 已注销、AWS 账号为零、GHCR/Oregon 已迁。

## 现状（前提）

| 已收敛 | 本文要抠 | 不在范围 |
|---|---|---|
| 发版 TAT；归档新写禁 AWS | 仍对腾讯机默认 SSM/OIDC 的运维入口；SecureString 活源 | Oregon、供应商 AWS、只碰 AWS 资源的 job、显式 legacy 应急壳 |

## 审批门禁

`approved_by: pending`。人改署名并 merge = 批准（`pending` 不得进 `main`）。  
批准前只改文档；批准后逐 PR 抠，禁止大爆炸。
