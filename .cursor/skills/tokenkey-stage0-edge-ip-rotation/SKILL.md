---
name: tokenkey-stage0-edge-ip-rotation
description: >-
  Deprecated TokenKey EC2 edge IP rotation path. Use only to redirect polluted edge egress IP rotation requests to tokenkey-stage0-edge-lightsail-ip-rotation.
---

# DEPRECATED：Edge EIP 污染轮换（EC2/CFN）

**已于 2026-06-07 退役。** TokenKey 的 EC2/CFN **Edge** 路径已整体移除，`deploy-edge-stage0.yml` 的 `rotate_egress_ip` 操作与相关 EIP 迁移/分配工具一并删除。edge 现在只有 Lightsail Static IP，没有 EC2 EIP 可轮换。

- **轮换被污染的 edge egress IP（唯一路径）：** 用 `tokenkey-stage0-edge-lightsail-ip-rotation`。
- **prod 不受影响**：prod 主网关仍是 EC2/CFN（且 prod IP 轮换本就不在 edge skill 范围内）。

历史 EC2 EIP 轮换 runbook 保留在 git 历史中（本文件此前版本）。
