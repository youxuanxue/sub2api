---
name: tokenkey-stage0-edge-platform-migration
description: >-
  DEPRECATED 2026-06-07 — the EC2/CFN Edge path was removed; edges are now
  Lightsail-only, so there is no EC2 edge left to migrate from. For adding a
  new edge use tokenkey-stage0-edge-lightsail-expansion. (prod remains
  EC2/CFN and is unaffected.)
---

# DEPRECATED：Edge 平台迁移（EC2 → Lightsail）

**已于 2026-06-07 退役。** TokenKey 的 EC2/CFN **Edge** 路径已整体移除（`deploy-edge-stage0.yml`、`stage0-edge-ec2.yaml`、EIP 轮换工具均已删除，`edge-targets.json` 清空）。edge 现在**只有 Lightsail 一条路径**，没有任何 EC2 edge 可供迁移，本「EC2 → Lightsail 迁移」流程不再适用。

- **新增 edge：** 用 `tokenkey-stage0-edge-lightsail-expansion`。
- **prod 不受影响**：prod 主网关仍是 EC2/CFN。

历史迁移 runbook 保留在 git 历史中（本文件此前版本）。
