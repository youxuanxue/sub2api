---
name: tokenkey-stage0-edge-expansion
description: >-
  DEPRECATED 2026-06-07 — the EC2/CFN Edge path was removed; edges are now
  Lightsail-only. To add a new edge use tokenkey-stage0-edge-lightsail-expansion.
  (prod remains EC2/CFN and is unaffected.)
---

# DEPRECATED：新增 EC2/CFN Edge 网关

**已于 2026-06-07 退役。** TokenKey 的 EC2/CFN **Edge** 路径已整体移除（`deploy-edge-stage0.yml`、`stage0-edge-ec2.yaml`、相关 EIP 轮换工具均已删除，`edge-targets.json` 清空）。新增 edge 不再走 EC2/CFN。

- **新增 edge（唯一路径）：** 用 `tokenkey-stage0-edge-lightsail-expansion`。
- **prod 不受影响**：prod 主网关仍是 EC2/CFN。

历史 EC2 edge 接入 runbook 保留在 git 历史中（本文件此前版本）。
