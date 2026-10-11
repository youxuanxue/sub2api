---
title: TokenKey 全线迁移 Hetzner Cloud（prod + edge · 德区 · cax21）
status: approved
approved_by: "feng (merge #2517, 2026-10-09)"
created: 2026-10-09
revised: 2026-10-11
owners: [tk-platform]
scope: >-
  deploy/hetzner/* + deploy-*-hetzner*.yml + Stage0 dispatch 路由；
  凭证与命令见 deploy/hetzner/README.md
related_prs: [2517, 2532]
related_designs:
  - docs/approved/deploy-stage0-workflow.md
  - docs/approved/edge-bluegreen-release-safety.md
  - docs/approved/design-fleet-pgdump-restore-canary.md
---

# TokenKey 全线迁移 Hetzner Cloud

> **`approved`（#2517）。** Phase-1 骨架已合入。  
> **操作 / 进度 / 切流清单以** [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md) **为执行 SSOT**（本文件锁决策；README 跟实测更新）。

## 本质：全新换机（不是原地升级）

每条 edge / 未来的 prod 都是 **新 arm64 主机 + 新本地 Postgres/Redis + 新出口 IP**：

| 做 | 不做 / 不假设 |
|---|---|
| 新 `cax21` 点火、SSM Hybrid、compose 拉起 | 把 Lightsail 磁盘「搬」到 Hetzner |
| precious-class `pg_dump` + logs data-only 整库复刻 | 只跑 `migrate-edge-accounts` 当全量 |
| 正式 A 切到新 IP；旧机 ≥7d 只读回滚 | 双活双写业务库 |
| Redis / 调度内存态在新机重建 | Redis 热迁移 |
| gemini-web / OAuth 会话因 **出口 IP 变** 常需 re-import | 「灌库等于会话仍活」 |

延迟与上游成功率不承诺与全球多区 Lightsail 一致。禁止「体验零影响」话术。

**Agent 编排：** [`.cursor/skills/tokenkey-host-replacement`](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)（跨平台换机门禁；平台命令仍以本文件决策 + README/DR runbook 为准）。

## 已锁定

| | |
|---|---|
| 去哪 | Hetzner **`fsn1`**，机型 **`cax21`**（4c/8G **arm64**） |
| 控面 | **SSM Hybrid**（蓝绿经 AWS SSM → `deploy_via_ssm_bluegreen.sh`；首发从单色 `tokenkey` 迁 blue） |
| 节奏 | **Edge-first（四边）→ prod（override）→ `us3`/`us6` 延期**：见 README Prod 节 |
| ⚠️ us3/us6 实际路径 | **2026-10-11 改为账号级退役，本基线的「重建 HZ + 整库复刻」未执行且已取消。** 账号折叠进既有 uk1/uk2（12+19 个），edge id 不保留，不重建机器。偏离经用户逐项确认；执行记录与坑位见 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md) 「us3/us6 账号级退役」节。 |
| 身份 | 逻辑 edge id / 正式域名**不变**；平台差靠 `*-hz-*` 与 staging |
| 数据 | **整库复刻**用舰队 precious-class `pg_dump` + 日志 data-only（见 README）；**禁止**只靠 `migrate-edge-accounts` 当「全量迁移」 |
| 切流序 | 未切边：**冻写 → 新鲜 dump/restore → 对账 → 正式 A**；已切边禁止再整库覆盖 live HZ |

**承诺：** 数据不丢（按 RPO/补漏纪律）、可回滚旧 IP、无长时间硬中断为目标。  
**不承诺：** 延迟/上游成功率与今日全球多区一致。

## 进度（2026-10-10 晚 · 实测）

| 项 | 状态 |
|---|---|
| uk1 / us4 / us5 / uk2 正式 DNS | **已切** Hetzner（A 见 README） |
| 四边 LS app | **已停写**（`tokenkey*` + gemini-web stop；PG/Caddy/Redis 保留 ≥7d；`.env` `TOKENKEY_LS_STANDBY_READONLY=1`） |
| us3 / us6 HZ | 不再建（账号级退役，2026-10-11）；LS 机器仍在跑但已无 prod stub，待 gemini re-import 后停写 |
| prod 正式 | **已切** `api.tokenkey.dev` → `167.233.211.115`；ops 控制面 Hybrid `mi-*`（param=`hetzner`）；AWS EIP standby；矩阵 `prod-target-hetzner.json` `deployable=true` |
| 库复刻 uk1/uk2/us4/us5 | precious + logs 已灌；切前未切边再刷见 README |
| us4 定点补漏 | append-only：ulog/dedup/ops_system（含停写前再补）；`post_missing=0` |
| uk1 补漏 | dump 水位后 LS 0 新行 → 无需补漏 |
| us4 `qa_records_202608` | 4034 行（全在该分区）已按运营确认删除 |
| 主机 parity（四边） | disk-metrics + ghcr-prune timer + Feishu webhook **live 已齐**；bootstrap 已嵌入同脚本 |
| gemini-web worker | arm64 已部署；**会话仍多需 re-import**（出口 IP） |
| prod stub 切流伤 | `base_url` 缺 `https://`（uk2/us5）已修；容器未吃 host pin 曾双写 LS → restart `tokenkey-green` 后停 |

**下一刀：** HCloud **蓝绿发版首跑**（prod `deploy-stage0` / edge `dispatch-edge-deploy` → `ensure_legacy_cutover`）；LS ≥7d 退役与 gemini re-import 见 README。us3/us6 重建已取消（账号级退役）。

## 切流实测教训（写进执行纪律）

1. **prod 容器 DNS：** host `/etc/hosts` pin **不会**自动进已运行的 `tokenkey-green/blue`；切流后若仍见 prod EIP 打旧 LS IP → `docker restart` 活动色，或 compose `extra_hosts`。  
2. **prod stub `base_url`：** 必须 `https://api-<edge>.tokenkey.dev`（裸域名 → `invalid base_url` / 502）。切流后同步 edge `api_keys` 时校验 scheme。  
3. **Caddy `API_DOMAIN`：** 双域名须加引号；`MAIN_GATEWAY_ALLOWED_CIDR`（prod `34.194.234.88/32`）空则 gateway 探针被拒。  
4. **已切边禁止整库重灌 live HZ**（会丢掉「只在 HZ」窗口）。LS 残留用 `request_id` 定点 `INSERT … NOT EXISTS`。  
5. **LS 停写：** 停 app/gemini-web，保留 PG/Caddy ≥7d；直打旧 IP 应无落库。  
6. **探针：** `run-probe --target edge:<id>` 仍解析 Lightsail → 对 HZ 直投 `mi-*`。  
7. **prod 不可调度 stub（cc/gemini-uk/grok）：** 根因是边侧无健康池/会话 paused，不是 DNS；勿盲目 `schedulable=true`（详见 README）。

## 历史：Phase-1 下一刀（已完成，留档）

**原目标：** staging 上跑通 **`uk1` Hetzner 边**——不切正式 DNS、不翻 `deployable=true`。

Owner：`deploy/hetzner/provision-edge.sh` + `render-bootstrap.sh`；IAM：`tokenkey-hetzner-ssm-hybrid-uk1`（CFN addon）。

| 步 | 做 | 停线 |
|---|---|---|
| 0 | 应用 CFN addon（含 Hetzner uk1 Hybrid role） | PassRole/create-activation 失败则停 |
| 1 | **L0**：Primary IP 空余 ≥2 | 不够就清闲置/提额 |
| 2 | `confirm_paid --tag X.Y.Z`：建机 + user-data（SSM Hybrid + compose） | 仍无正式 DNS |
| 3 | 等 mi-* + postgres/settings 绿；记下 public IP | 起不来查 `/var/log/tokenkey-hetzner-bootstrap.log` |
| 4 | Staging DNS：`api-uk1-hz.tokenkey.dev` A → 该 IP | ACME 需要 |
| 5 | **E0** arm64；**E1** staging smoke full | 红则修；不 mirror、不切正式 DNS |

## 以后才做

| 何时 | 做什么 | 门禁 |
|---|---|---|
| **prod 蓝绿发版** | 正式 DNS 已切；首发从单色迁 blue，之后与 Lightsail 同 primitive | `deploy-stage0` + `STAGE0_DEPLOY_PROFILE=prod` |
| ~~**us3**（延期）~~ **已取消** | 2026-10-11 账号级退役替代：12 账号 → uk1，prod stub 全切/软删，不重建机器 | README「us3/us6 账号级退役」 |
| ~~**us6**（延期）~~ **已取消** | 同上：19 账号 → uk2 | 同上 |
| gemini-web / 供应 stub | 各 HZ 边会话 re-import；按需修 anthropic/grok 池后再开 prod stub | 冒烟 200 才 `schedulable=true` |
| 另审批 | 升配 / 去 SSM / 多区出口 | Phase-5 |

硬约束（全程）：禁止双写业务库；Redis 不迁；Secrets 不进 git（`/tokenkey/hetzner/…`）；Lightsail/EC2 停机保留 ≥7 天再退役；**live HZ 禁止再用 LS 全量 dump 覆盖**（会丢掉切后新账）。

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
