---
title: 运维旁路与密钥材料收敛到腾讯云（TAT / COS）
status: approved
approved_by: "feng（2026-11-18 会话：同意。请推进实现。）"
approved_at: 2026-11-18
risk: high
authors: [agent]
created_at: 2026-11-18
revised: 2026-11-20（压缩复审散文；纪律与门牙保留）
---

# 运维旁路与密钥材料收敛到腾讯云（TAT / COS）

## 用户决定（2026-11-18）

原话：「运维旁路与密钥材料，也应该改到 腾讯 tat 或者 cos 等，收敛到腾讯云体系内。」

## 纪律与门牙

1. **管腾讯机 = TAT**（既有 `remote_exec` + `tencent-ops`）。prod / cn-prod 日常默认不得再走 Hybrid SSM / OIDC。
2. **ops 密钥 = `tencent-ops` secret、主机 root-only、或腾讯 COS**。SecureString 停写；迁完停读再删。离机材料对齐 `design-cos-object-archive.md`。
3. **门牙**：OIDC→SSM 管腾讯机，或 ops 钥写入 SecureString → **合不进来**。无「先写 AWS 再迁」开关。

不做：迁移分期百科、密钥新产品、迁 GHCR/清 Oregon、改拓扑/重开蓝绿、break-glass 写进产品默认、未批准就拆生产。Hybrid 注销另批。

完成：门牙合入；日常运维默认无 `configure-aws-credentials`；活 ops 钥 SSOT 非 SecureString；舰队 README 与本文一致。不宣称 Hybrid 已注销或 AWS 账号为零。

## 现状（前提）

| 已收敛 | 本文要抠 | 不在范围 |
|---|---|---|
| 发版 TAT；归档新写禁 AWS | 仍对腾讯机默认 SSM/OIDC 的运维入口；SecureString 活源 | Oregon、供应商 AWS、只碰 AWS 资源的 job、显式 legacy 应急壳 |

## 审批门禁

已于 2026-11-18 批准。实现按 `rg` 命中逐 PR 抠；先门牙（禁新增），再旁路/密钥。禁止大爆炸。
