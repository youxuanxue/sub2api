---
name: tokenkey-host-replacement
description: >-
  TokenKey fresh-host replacement (prod): new machine + dump-restore + DNS
  cutover + old-host standby. Use for 换机/整机迁移/prod EC2 reprovision.
  Edge deploy retired 2026-10-17 — not routine edge fan-out, image-only deploy,
  or IP-only rotation.
---

# TokenKey：全新换机

编排：**新主机 + 整库复刻 + 切流 + 旧机 ≥7d**。

活舰队：[`deploy/hetzner/README.md`](../../../deploy/hetzner/README.md)。踩坑：[`references/playbook.md`](references/playbook.md)。

## 触发 / 勿用

触发：prod 换机、整机迁移、reprovision。

勿用→镜像 `stage0-release-rollout`；LS 换 IP→`lightsail-ip-rotation`；仅 accounts→`migrate-edge-accounts`；prod 卷在→HZ README 恢复。

**Edge：** 默认不部署/不 fan-out（矩阵 `deployable=false`；prod stub 已软删）。恢复 edge 须单独批准。

## 文档

| 资产 | 职责 |
|---|---|
| 本 skill | 门禁/不变量 |
| `references/playbook.md` | 切流 + 硬坑 |
| `docs/approved/hetzner-cloud-full-migration.md` | 决策锁 |
| `deploy/hetzner/README.md` | 舰队 + 发版 SSOT |
| `deploy/aws/RUNBOOK-disaster-recovery.md` | standby / 离机 S3 |
| `docs/ops/aws-tokyo-cutover-20261017.json` | 最近切流回执 |

## 不变量

禁双写；活库禁旧机全量覆盖；Redis 不迁；旧机 ≥7d；`API_DOMAIN` 单正式名；冻结只用 `ALTER DATABASE … read_only`；激活网络带 compose labels；Caddy upstream = `tokenkey-blue|green`。

## 阶段

`target=prod` · `platform=ec2` · `phase=plan|provision|replicate|cutover|drain|verify|full`

1. plan — IP/配额/RPO/授权/回滚  
2. provision — 新 EC2；hold + internal 网络  
3. replicate — precious+logs→restore→对账  
4. cutover — 冻结 → final delta → 激活（label 安全开网）→ 正式 A → README/CIDR  
5. drain — 停旧 app；standby；**单独批准**后再关机  
6. verify — dig@8.8.8.8；`--resolve` smoke；pgdump timer；README 活身份

报告：`target platform phase new_ip old_ip dump checks dns drain smoke followups`
