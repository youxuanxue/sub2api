---
name: tokenkey-stage0-edge-ip-rotation
description: >-
  Use when a TokenKey Edge egress IP is polluted and the current deployable owner may be Lightsail or EC2.
---

# Edge Egress IP 污染轮换路由

先用 `scripts/stage0/resolve-edge-deploy-route.py --edge-id <id> --json` 解析当前 deployable owner。

- Lightsail owner：用 `tokenkey-stage0-edge-lightsail-ip-rotation`。
- EC2 owner/candidate：当前没有受支持的自动 EIP 轮换入口；不要释放或替换迁移绑定的 EIP。
- EC2 需要换 IP 时属于新的迁移/回滚设计，先暂停并取得高风险审批。

prod IP 轮换不在本 skill 范围。
