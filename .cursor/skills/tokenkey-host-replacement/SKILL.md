---
name: tokenkey-host-replacement
description: >-
  TokenKey fresh-host replacement (edge/prod): new machine + precious/logs
  dump-restore + DNS cutover + old-host standby. Use for 换机、整机迁移、
  Hetzner/Lightsail reprovision、us3/us6/prod; not image-only deploy, IP-only
  rotation, or accounts-only migrate.
---

# TokenKey：全新换机

编排：**新主机 + 整库复刻 + 切流 + 旧机 ≥7d**。平台命令见下方文档。

## 触发 / 勿用

触发：换机、整机迁移、reprovision、LS→HZ、`us3`/`us6`/prod。

勿用→改用：镜像→`stage0-release-rollout`；LS 换 IP→`lightsail-ip-rotation`；仅 accounts→`migrate-edge-accounts`；prod 卷在→DR §3；新 LS edge→`lightsail-expansion`。

## 文档

| 资产 | 职责 |
|---|---|
| 本 skill | 门禁/不变量/坑位 |
| `docs/approved/hetzner-cloud-full-migration.md` | 决策锁 |
| `deploy/hetzner/README.md` | HZ 执行+教训 SSOT |
| `deploy/aws/RUNBOOK-disaster-recovery.md` | prod / §4.4 |
| fleet pgdump canary approved | precious 契约 |

优化：先改本 skill，再回写 README/approved。

## 不变量

禁双写；已切 live **禁**源侧全量覆盖（只 `request_id` 补漏）；Redis 不迁；旧机 ≥7d；DNS/冻写/付费建机先 plan；stub 必须 `https://`；prod pin 后 restart 活动色（或 `extra_hosts`）。

## 阶段

`target=edge:<id>|prod` `platform=hetzner|lightsail|ec2`  
`phase=plan|provision|replicate|cutover|drain|verify|full`

1. plan — IP/配额/RPO/授权/回滚（A→旧 IP）  
2. provision — HZ README；LS expansion（`recreate` 须明示）；staging 先绿  
3. replicate — precious+logs→restore→对账；未切流才可整库重灌  
4. cutover — 正式 A；双域名；CIDR；prod pin+restart；stub `https://`  
5. drain — 停旧 app；standby 标记；源 usage→0  
6. verify — dig@8.8.8.8；smoke；host units；gemini re-import；HZ 用 `mi-*`

Prod：卷在→DR §3；无卷/跨云→§4.4。骨架：[references/playbook.md](references/playbook.md)。

## 坑位（2026-10）

DNS 缓存→restart/`extra_hosts`；stub 无 scheme→`https://`；已切边禁整库重灌；CIDR 空→prod EIP/32；空池 stub→先修池；`run-probe edge:*` 在 deployable 后 `auto`→HZ。  
prod 切流：同机四 hostname + 正式 Caddy（非 staging edge）+ Edge CIDR；bootstrap 带 pgdump/disk-metrics/ghcr-prune；Wave A 禁 live `QA_BUNDLE_*`。us3/us6 defer 以 README 为准。详见 `WAVE-B-PROD-CUTOVER-RUNBOOK.md`。

报告：`target platform phase new_ip old_ip dump checks dns drain smoke followups`
