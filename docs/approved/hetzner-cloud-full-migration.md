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

> **`approved`（#2517）。** Phase-1 骨架已合入；**正式 DNS / 生产切流仍须 Gates 全绿。**  
> 决策与门禁在此；操作见 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)。

## 目标

prod + 全部 deployable edge → **Hetzner `fsn1` / `cax21`（4c/8G arm64）**；过渡期 `hcloud` + AWS SSM Hybrid，复用蓝绿/烟测。

- **承诺：** 数据不丢；无长时间硬中断；可回滚。
- **不承诺：** 延迟/上游成功率与今日全球多区一致。禁止「体验零影响」。

**非目标：** 不改业务/定价/候选 SSOT；不强制托管 PG/Redis；不第一天关 AWS；过渡期不做全球就近出口。

## 已锁定

| ID | 选择 |
|---|---|
| D1 | **SSM Hybrid**（改原生 Hetzner 控面另批） |
| D2 | **arm64 / `cax21`** |
| D3 | **Edge-first**（`uk1` → `us5` → 其余 → prod） |
| D4 | **`fsn1`**（prod+edge 同区） |
| D5 | 全舰队 **`cax21`** |

逻辑 edge id / 正式域名不变（`uk1`…`us6`）。平台差靠 `*-hz-*` 主机名与 staging，不另起 id。

## 硬约束

| 规则 | 做法 |
|---|---|
| 禁止双写 | 任意时刻一个 prod PG 写 |
| 回滚两档 | 写前 DNS 回旧机；写后禁裸回，留新库或先回灌 |
| Edge | staging → mirror → DNS；Lightsail 停机 ≥7 天 |
| Prod | 冻写 ≤5 min → dump/restore/游标/计费冒烟 → DNS |
| Redis | 不迁 |
| Secrets | 不进 git；SSM `/tokenkey/hetzner/…` |

## 阶段与停线

`0 凭证 → 1 骨架 → 2 canary → 3 全 edge → 4 prod → 5 升配/控面（另批）`

| 动作 | 前提 |
|---|---|
| Phase-1 骨架 | 可做（进行中；矩阵全 `deployable=false`） |
| 付费 canary | **L0** 绿 |
| Edge 正式 DNS | **E0–E4** 绿 |
| 下一台 Edge | 上台 **E5** 绿满 60 min + L0 |
| Prod DNS | **P1–P4、P6** 绿；值守 P5 |

## Gates

测 → 红只做对策 → 绿才下一步。复用现有 smoke / probe / pgdump / mem-guard / `hcloud`。

| ID | 门禁 | 红灯 |
|---|---|---|
| L0 | Primary IP 空余够（canary ≥2） | 清闲置/提额；禁硬扛 403 |
| E0 | 镜像 arm64 | 换 multi-arch；禁 `simple_release` |
| E1 | staging smoke full | 修 bootstrap；不切 DNS |
| E2 | OAuth 探针成功 | 换 Floating IP；仍红则留 AWS |
| E3 | p95 ≤ 旧基线 ×2 | 暂留 AWS 或签字接受 |
| E4 | MemAvailable 尖峰 ≥1.5 GiB | 降并发；不切 |
| E5 | DNS 后 60 min 错率 ≤ 基线 +2pp | 立刻回 Lightsail |
| P1 | restore 成功 | 不进窗 |
| P2 | pgdump→S3→canary 绿 | 不切 DNS |
| P3 | Volume snapshot available | 无快照不开窗 |
| P4 | 游标一致 + 计费冒烟 | 解冻中止 |
| P5 | 切后 60 min ≤ 基线 +2pp | 写前回 EC2；写后禁裸回 |
| P6 | 冻写 ≤5 min | 超时解冻 |

Edge 顺序：L0 → provision → E0 → E1–E4 → 1 账号 mirror ≥2h → 全量 mirror → DNS → E5。

## 审批

- [x] D1/D2/D4/D5（2026-10-09）
- [ ] D3 Edge-first
- [ ] 接受德区出口的 RTT/风控变化
- [ ] 接受「数据+回滚」优先于「全球最低延迟」
- [ ] 凭证就绪（见 README）
