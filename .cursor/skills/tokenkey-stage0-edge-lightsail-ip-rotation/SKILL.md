---
name: tokenkey-stage0-edge-lightsail-ip-rotation
description: >-
  Rotate a reviewed, deployable Lightsail edge Static IP after pollution.
  The current edge fleet is retired; retired and planned targets are rejected.
  Japanese egress proxies are not edge gateways.
---

# Lightsail Edge 静态 IP 轮换

当前无可用 edge 目标，身份见 [舰队清单](../../../deploy/hetzner/README.md)。
旧 edge 已退役，日本出口代理不属于本工具。只有另行批准的新身份进入活矩阵后才适用；
不要修改退役行或用 `--allow-planned` 绕过。镜像部署走 `tokenkey-stage0-release-rollout`。

## Plan

用 git-worktree-submodule 技能准备与主线一致的独立工作区，再运行：

```bash
bash ops/lightsail/rotate-static-ip.sh <edge_id>
```

脚本通过共享 Lightsail resolver 检查退役状态、可部署性及完整身份，在任何 AWS 调用前
拒绝无效目标。默认只读，输出实际旧 IP 和操作计划。核对账户、实例、旧 IP 及污染证据。

## Swap

用户批准计划后执行（旧 IP 最终会释放，不能保证重新取回）：

```bash
bash ops/lightsail/rotate-static-ip.sh <edge_id> --apply --reason '<污染证据>'
```

脚本负责申请候选 IP、排除已污染地址、切换绑定、更新 SSM public_ip、释放旧 IP、登记污染记录。
失败先核对当前绑定，不盲目重复。同步矩阵中的 `static_ip_name` / `porkbun_a_ipv4`，
通过 `scripts/edge-ip-status.sh --check` 后提交。

## DNS 与验证

按已批准范围使用 `ops/dns/porkbun-upsert-a.sh` 更新 DNS；检查公网解析并用
`curl --resolve <domain>:443:<new_ip> https://<domain>/health` 验证指定出口。
需要证书/端口检查时使用 `ops/stage0/verify-edge-lightsail-network.sh`。
发布 smoke 使用 `scripts/stage0/dispatch-edge-deploy.sh`，遵循恢复确认门禁；
不能从 DNS 解析成功推断上游账号已恢复，最后以真实请求日志核对效果。
