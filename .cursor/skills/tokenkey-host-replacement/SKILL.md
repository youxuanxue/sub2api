---
name: tokenkey-host-replacement
description: >-
  TokenKey fresh-host replacement (edge/prod): new machine + precious/logs
  dump-restore + DNS cutover + old-host standby. Use for 换机、整机迁移、
  Hetzner/Lightsail reprovision、prod; not image-only deploy, IP-only
  rotation, or accounts-only migrate.
---

# TokenKey：全新换机

编排：**新主机 + 整库复刻 + 切流 + 旧机 ≥7d**。

活舰队 SSOT：[`deploy/hetzner/README.md`](../../../deploy/hetzner/README.md)。

## 触发 / 勿用

触发：换机、整机迁移、reprovision。

勿用→改用：镜像→`stage0-release-rollout`；LS 换 IP→`lightsail-ip-rotation`；仅 accounts→`migrate-edge-accounts`；prod 卷在→HZ README 恢复入口；仅明确旧 LS 操作→`lightsail-expansion`。

## 文档

| 资产 | 职责 |
|---|---|
| 本 skill | 门禁/不变量 |
| `docs/approved/hetzner-cloud-full-migration.md` | 决策锁 |
| `deploy/hetzner/README.md` | 现网舰队 + 发版 SSOT |
| `deploy/aws/RUNBOOK-disaster-recovery.md` | AWS standby / 离机 S3 读取（不是 HZ 卷恢复） |
| fleet pgdump canary approved | precious 契约 |

## 不变量

禁双写；活库禁旧机全量覆盖；Redis 不迁；旧机 ≥7d；stub `https://`；CIDR/SSM 见 Hetzner README 活舰队；`API_DOMAIN` 单正式名。

## 阶段

`target=edge:<id>|prod` `platform=hetzner|lightsail|ec2`  
`phase=plan|provision|replicate|cutover|drain|verify|full`

1. plan — IP/配额/RPO/授权/回滚  
2. provision — HZ README；LS expansion（`recreate` 须明示）  
3. replicate — precious+logs→restore→对账；仅未上线目标可整库重灌  
4. cutover — 正式 A；单域名；CIDR；prod pin+restart；stub `https://`  
5. drain — 停旧 app；standby；源 usage→0  
6. verify — dig；smoke；host units；gemini re-import；README 活身份

Prod：现网 fresh AWS EC2；历史 Hcloud 卷见 Hetzner README；无卷/跨云先核离机 dump。骨架：[references/playbook.md](references/playbook.md)。

报告：`target platform phase new_ip old_ip dump checks dns drain smoke followups`
