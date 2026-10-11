---
title: TokenKey 全线 Hetzner Cloud 舰队（prod + edge · 德区 · cax21）
status: approved
approved_by: "feng (merge #2517, 2026-10-09)"
created: 2026-10-09
revised: 2026-10-11
owners: [tk-platform]
scope: >-
  deploy/hetzner/* + deploy-*-hetzner*.yml + Stage0 dispatch 路由；
  现网执行见 deploy/hetzner/README.md
related_prs: [2517, 2532, 2550, 2551, 2556]
related_designs:
  - docs/approved/deploy-stage0-workflow.md
  - docs/approved/edge-bluegreen-release-safety.md
  - docs/approved/design-fleet-pgdump-restore-canary.md
---

# TokenKey Hetzner Cloud 舰队（决策锁）

> **`approved`。** 现网执行 SSOT：[`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)。本文件只锁决策与不变量；**不**保留切流过程史。

## 现网范围

活舰队五节点：**prod + uk1 + uk2 + us4 + us5**（Hetzner `fsn1` / `cax21` / arm64 / SSM Hybrid）。  
`us3` / `us6` **不在舰队**（不重建、无正式 DNS）。IP / `mi-*` / 发版入口见 README。

## 已锁定

| | |
|---|---|
| 去哪 | Hetzner **`fsn1`**，机型 **`cax21`**（4c/8G **arm64**） |
| 控面 | **SSM Hybrid**；蓝绿经 AWS SSM → `deploy_via_ssm_bluegreen.sh`（首发单色 `tokenkey` → blue） |
| 身份 | 逻辑 edge id / 正式域名不变；稳态 `API_DOMAIN` 单正式名 |
| 数据 | 换机整库复刻用 precious-class `pg_dump` + logs data-only；**禁止**只靠 `migrate-edge-accounts` 当全量 |
| live 库 | **禁止**用旧机全量 dump 覆盖已在服务的 HZ |
| 双写 | 禁止业务库双活双写；Redis 不热迁 |
| Secrets | `/tokenkey/hetzner/…`；不进 git |
| 旧机 | Lightsail / AWS 停写后 ≥7d 再退役 |

延迟与上游成功率不承诺与全球多区一致。换机编排：[`.cursor/skills/tokenkey-host-replacement`](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)。

## 新主机门禁（复用）

| ID | 门禁 | 红灯 |
|---|---|---|
| L0 | Primary IP 空余够 | 清闲置/提额 |
| E0 | 镜像 arm64 | 换 multi-arch；禁 `simple_release` |
| E1 | smoke / health | 修 bootstrap；勿指正式流量 |
| E2 | OAuth 探针（若该边需要） | 换出口或停切 |
| E4 | MemAvailable 尖峰 ≥1.5 GiB | 降并发 |
| E5 | 切 DNS 后错率 | 立刻回滚 A |
