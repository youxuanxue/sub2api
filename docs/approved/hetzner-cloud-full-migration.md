---
title: TokenKey 全线迁移 Hetzner Cloud（prod + edge · 德区 · cax21）
status: approved
approved_by: "feng (merge #2517, 2026-10-09)"
created: 2026-10-09
revised: 2026-10-09
owners: [tk-platform]
scope: >-
  deploy/hetzner/* + deploy-*-hetzner*.yml + Stage0 dispatch 路由；
  凭证与命令见 deploy/hetzner/README.md
related_prs: [2517]
related_designs:
  - docs/approved/deploy-stage0-workflow.md
  - docs/approved/edge-bluegreen-release-safety.md
  - docs/approved/design-fleet-pgdump-restore-canary.md
---

# TokenKey 全线迁移 Hetzner Cloud

> **`approved`（#2517）。** Phase-1 骨架已合入。  
> 操作命令见 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)。

## 已锁定

| | |
|---|---|
| 去哪 | Hetzner **`fsn1`**，机型 **`cax21`**（4c/8G **arm64**） |
| 控面 | **SSM Hybrid**（蓝绿仍走 AWS SSM） |
| 节奏 | **Edge-first**：`uk1` → `us5` → 其余 edge → prod |
| 身份 | 逻辑 edge id / 正式域名**不变**；平台差靠 `*-hz-*` 与 staging |

**承诺：** 数据不丢、无长时间硬中断、可回滚。  
**不承诺：** 延迟/上游成功率与今日全球多区一致。禁止「体验零影响」。

## 下一刀（唯一）

**目标：** staging 上跑通 **`uk1` Hetzner 边**——**不切正式 DNS、不翻 `deployable=true`。**

| 步 | 做 | 停线 |
|---|---|---|
| 1 | **L0**：Primary IP 空余 ≥2 | 不够就清闲置/提额，禁硬扛 403 |
| 2 | `confirm_paid` 建 `uk1`（`tokenkey-edge-uk1-hz-cax21`） | 仍无 DNS |
| 3 | SSM Hybrid + compose 拉起 → `api-uk1-hz.tokenkey.dev` | 起不来不进测 |
| 4 | **E0** 镜像 arm64；**E1** staging smoke full | 任一红 → 修，不 mirror、不切 DNS |

**本刀交付物：** L0→E1 绿证（可贴 PR / ops 笔记）。到此为止。

**本刀不做：** E2 上游封 IP、mirror、正式 DNS、`us5`、其他 edge、prod、升配、去 SSM。

## 以后才做（别和下一刀混）

| 何时 | 做什么 | 门禁 |
|---|---|---|
| uk1 E0–E1 绿之后 | E2–E4 → 1 账号 mirror ≥2h → 正式 DNS → E5 60 min | 见附录 |
| uk1 E5 绿满后 | `us5` 同流程（跨洋），再滚其余 edge | 上台 E5 满 60 min 才开下一台 |
| 全 edge 稳后 | prod：Volume + P1–P4/P6 → 冻写 ≤5 min 切流 → P5 | 写后禁裸 DNS 回旧库 |
| 另审批 | 升配 / 去 SSM / 多区出口 | Phase-5 |

硬约束（全程）：禁止双写业务库；Redis 不迁；Secrets 不进 git（`/tokenkey/hetzner/…`）；Lightsail/EC2 停机保留 ≥7 天再退役。

## 附录：Gates 速查

复用现有 smoke / probe / pgdump / mem-guard / `hcloud`。测 → 红只做对策 → 绿才下一步。

| ID | 门禁 | 红灯 |
|---|---|---|
| L0 | Primary IP 空余够 | 清闲置/提额 |
| E0 | 镜像 arm64 | 换 multi-arch；禁 `simple_release` |
| E1 | staging smoke full | 修 bootstrap；不切 DNS |
| E2 | OAuth 探针成功 | 换 Floating IP；仍红则留 AWS |
| E3 | p95 ≤ 旧基线 ×2 | 暂留 AWS 或签字接受 |
| E4 | MemAvailable 尖峰 ≥1.5 GiB | 降并发；不切 |
| E5 | DNS 后 60 min 错率 ≤ 基线 +2pp | 立刻回 Lightsail |
| P1–P4,P6 | restore / 备份 / snapshot / 游标+计费 / 冻写≤5 min | 不进窗或不切 DNS |
| P5 | 切后 60 min ≤ 基线 +2pp | 写前回 EC2；写后禁裸回 |
