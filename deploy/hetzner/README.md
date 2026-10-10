# TokenKey Edge / Prod on Hetzner Cloud

审批基线：[`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)

## 当前状态（2026-10-10；us5/uk2 正式 A 已切 HZ）

| edge | 正式 A（`api-<id>.tokenkey.dev`） | Hetzner IP | Lightsail IP（回滚） | 库复刻 |
|------|-----------------------------------|------------|----------------------|--------|
| **uk1** | **已切** → `188.245.112.147` | 同左 | `18.135.0.171`（app 已停写，PG/Caddy 保留 ≥7d） | precious + logs 已灌；水位对齐后**无需补漏** |
| **us4** | **已切** → `188.245.14.132` | 同左 | `32.188.80.151`（app 已停写，PG/Caddy 保留 ≥7d） | precious + logs 已灌；已定点补漏 42×ulog+42×dedup（≈$0.62） |
| **us5** | **已切** → `91.98.83.56`（冻写重灌后切） | 同左 | `16.144.175.131`（app 已停写，PG/Caddy 保留 ≥7d） | 2026-10-10 `T101255Z`+`logs-T101315Z` |
| **uk2** | **已切** → `2.31.27.73`（冻写重灌后切） | 同左 | `51.24.28.148`（app 已停写，PG/Caddy 保留 ≥7d） | 2026-10-10 `T101627Z`+`logs-T101629Z` |
| **us3** | LS `18.216.113.132` | **无机器**（曾建后删，腾配额） | 正式仍 LS | 待重建 + 全量复刻 |
| **us6** | LS `3.147.98.112` | **未起**（配额） | 正式仍 LS | 待 provision + 全量复刻 |
| **prod** | 仍 AWS Stage0 | staging 路径已接线 | — | **未做** P1 dump/restore / 冻写 / 正式 DNS |

Lightsail ≥7 天保留作回滚；禁止双写业务库。Redis 不迁（可重建）。

**本质：** 每条边都是 **全新换机**（新 cax21 + 新库 + 新出口 IP），不是 Lightsail 原地升级。决策锁在审批基线「本质：全新换机」节。

## 切流实测教训（2026-10-10）

| 坑 | 现象 | 处置 |
|----|------|------|
| prod 容器 DNS | host `/etc/hosts` pin 后，已跑的 `tokenkey-green` 仍解析旧 LS → us4/us5 LS 继续落 `usage`（来源 `34.194.234.88`） | `docker restart` 活动色；发版考虑 `extra_hosts` |
| stub `base_url` 缺 scheme | `invalid base_url: api-uk2…/antigravity`、`api-us5…` → prod 502 | 一律 `https://api-<edge>.tokenkey.dev`；同步 key 后校验 |
| Caddy `remote_ip` / CIDR | `MAIN_GATEWAY_ALLOWED_CIDR` 空 → gateway 探活被拒 | 设 prod EIP `/32`；改 `.env` 后 sync/restart caddy |
| 已切边整库重灌 | 丢掉「只在 HZ」窗口账 | **禁止**；只用 `request_id` 定点补漏 |
| LS 停写 | 四边 app+gemini-web 已 stop；PG/Caddy 留 ≥7d | `.env` `TOKENKEY_LS_STANDBY_READONLY=1` |
| prod stub `cc-*` / `gemini-uk*` / `grok-*` | `schedulable=f` | **边侧无健康池/会话 paused**，非 DNS；冒烟 200 前勿强开 |
| `run-probe edge:<id>` | 仍打到 Lightsail | HZ 直投 Hybrid `mi-*` |
| us4 `qa_records_202608` | 4034 行无业务价值 | 已按确认 `DELETE` |

## 文件

| 路径 | 作用 |
|---|---|
| `edge-targets-hetzner.json` | edge 矩阵（defaults + edge id） |
| `prod-target-hetzner.json` | prod 目标（Volume 强制；`deployable=false`） |
| `resolve-edge-hetzner-target.py` / `resolve-prod-hetzner-target.py` | 派生命名 + 硬门禁 |
| `render-bootstrap.sh` / `render-prod-bootstrap.sh` | Ubuntu user-data（SSM + compose；prod 挂 Volume） |
| `generated-user-data.sh` / `generated-prod-user-data.sh` | 渲染产物（须与 `--check` 同步提交） |
| `provision-edge.sh` / `provision-prod.sh` | dry-run 或 `--confirm-paid --tag` 点火 |

账号级搬运（**不是**整库）：`ops/migration/migrate-edge-accounts.py`（仅 accounts/groups/api_keys/PEC）。  
整库真相：舰队 `tokenkey-pgdump.sh` + runbook §4.4（见下「数据复刻」）。

## Edge 用法

```bash
gh secret set HCLOUD_TOKEN --body "$HCLOUD_TOKEN"

bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id uk1 --operation provision --platform hetzner --allow-planned

bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id uk1 --operation provision --platform hetzner --allow-planned \
  --confirm-paid --tag X.Y.Z

python3 deploy/hetzner/resolve-edge-hetzner-target.py --edge-id uk1 --allow-planned
bash deploy/hetzner/render-bootstrap.sh --check
bash deploy/hetzner/provision-edge.sh --edge-id uk1 --allow-planned
```

## 数据复刻（必做；勿只跑 migrate-edge-accounts）

### 两类 dump

| 类型 | 工具 / 命令 | 覆盖 | 不含 |
|------|-------------|------|------|
| **precious-class** | LS 上 `/usr/local/bin/tokenkey-pgdump.sh` → `s3://…/edge/<id>/pgdump/tokenkey-YYYYMMDDTHHMMSSZ.sql.gz` | 全表 schema + 除大体量日志外的全部数据（含 `usage_billing_dedup`、accounts、settings、audit、monitor 聚合等） | `usage_logs*` / `ops_error_logs*` / `ops_system_logs*` / `qa_records*` **行数据**（schema 仍在） |
| **logs data-only** | `pg_dump --data-only -t 'usage_logs*' -t 'ops_error_logs*' -t 'ops_system_logs*' -t 'qa_records*'` → `tokenkey-logs-*.sql.gz` | 上述历史日志行 | — |

Restore 目标：Hetzner Hybrid `mi-*`。PG18 dump 含 `\restrict`：灌库前 `sed` 去掉；函数冲突时用 `CREATE OR REPLACE FUNCTION`。  
`TRUNCATE … CASCADE` 可能波及 `billing_usage_entries`——先确认两边行数再截断。

### 本轮已灌时间点（UTC，2026-10-10）

| edge | precious 源 dump | logs 源 dump | 备注 |
|------|------------------|--------------|------|
| uk1 | `…T094328Z` | `…T095810Z` | 正式已在 HZ；LS 停写后对账无漏 |
| uk2 | 切前再刷 `…T101627Z` | `…logs-T101629Z` | 正式已在 HZ；见下「增量」 |
| us4 | `…T094154Z` | `…T095813Z` | 正式已在 HZ；见下定点补漏 + LS 停写 |
| us5 | 切前再刷 `…T101255Z` | `…logs-T101315Z` | 正式已在 HZ；见下「增量」 |

### 增量要不要搞？

**要。** dump 之后 Lightsail 上的新写入不会自动出现在 Hetzner。

| 边状态 | 增量策略 |
|--------|----------|
| **未切流**（未来 us3/us6） | 切流前做一次 **冻写窗内的新鲜 precious + logs** 整库重灌（或等价增量），然后 **立刻** 改正式 A。不要「先切 DNS 再慢慢补库」。 |
| **已切流**（uk1/us4/us5/uk2） | 正式流量已写 HZ。uk1/us4：prod 容器曾因切流前启动未吃到 host pin，us4 `/v1/responses` 仍打 LS——已 restart 纠正；LS app/gemini-web 已停（PG/Caddy 保留 ≥7d）；按 `request_id` 定点补漏见下表。**不要**再对已切边做整库覆盖。 |

禁止双活双写：增量窗口内正式流量只能打一侧。

### 已切边 LS→HZ 定点补漏（2026-10-10 实测）

只读比对 + append-only `INSERT … WHERE NOT EXISTS` / `ON CONFLICT DO NOTHING`（按 `request_id`）；**未** `DROP DATABASE` / 整库覆盖 / truncate live HZ。工件：`s3://…/edge/us4/pgdump/delta-backfill/` 与 `…/backfill-tmp/20261010T103620Z/`。

| edge | 结论 | 已回填 | LS 停写 |
|------|------|--------|---------|
| **uk1** | dump 水位后 LS **0** 新行；HZ 水位更高 | **无需补漏** | app+gemini-web 已 `docker stop`；PG/Caddy/Redis 保留 |
| **us4** | 曾双写（prod 容器 DNS）；水位后多轮补漏 | 早轮 `ulog` **520** + `dedup` **742** + `ops_system` **5**；停写前再补 **42**×ulog+**42**×dedup（≈$0.62，`post_missing=0`） | 同上；`.env` 标 `TOKENKEY_LS_STANDBY_READONLY=1` |
| **us5 / uk2** | 切流后短窗 prod→LS 残留（同 EIP）；TTL/连接排空后停 | 未做大额补漏（切前已冻写重灌） | 同 uk1：app+gemini-web 已停写 |

残留风险：Caddy 仍可能收到直打旧 IP（现应 502、无落库）；≥7d 后可删实例。此前整库重灌丢掉的 HZ-only 窗口 **不** 从 LS 恢复。prod 发版重建容器时确认吃到 host `/etc/hosts` pin（或 `extra_hosts`）。

## 增量 + DNS 切流推荐顺序

对 **尚未切正式 DNS** 的边（当前：us3/us6）：

1. Staging / 探针绿（E0–E4 级）。  
2. **冻写**（短）：停 LS 上该边业务写入路径，或接受秒～分钟级 RPO 并加速执行 3–5。  
3. LS：新鲜 `tokenkey-pgdump.sh` + logs data-only → S3。  
4. HZ：precious restore（`DROP DATABASE` / `TEMPLATE template0`）→ logs restore（`session_replication_role=replica` + 明确 `TRUNCATE` 日志父表）。  
5. 行数对账：`accounts` / `usage_billing_dedup` / `usage_logs` / `ops_*`。  
6. Porkbun：`api-<edge>.tokenkey.dev` A → Hetzner IP；prod `/etc/hosts` 可临时 pin。  
7. 双域名 Caddy + ACME；smoke；E5 soak。  
8. Lightsail 保留 ≥7 天只读/回滚，勿继续当正式写库。

对 **已切** 边：只做定点补漏或接受 dump→切流窗口 RPO；禁止再次整库覆盖 live HZ。

## 主机侧缺口（HCloud vs Lightsail，2026-10-10）

HZ 已有：compose 单色 `tokenkey` + postgres/redis/caddy + gemini-web；`tokenkey-pgdump.timer` + S3（Hetzner Hybrid IAM 已补 `edge/<id>/pgdump`）；precious+logs 库数据。

HZ **相对 LS 仍缺 / 不同**：

| 项 | Lightsail | Hetzner | 影响 |
|----|-----------|---------|------|
| 蓝绿 | `docker-compose.bluegreen.yml` + blue/green 单元 | 单容器 `tokenkey` | 发版路径不同；勿假设 bluegreen |
| `tokenkey-disk-metrics.timer` | 有（飞书盘/内存告警） | bootstrap 已装；**四边 live 已验证** | 缺 webhook 时 timer 静默 no-op |
| `tokenkey-ghcr-prune-daily.timer` | 有 | bootstrap 已装；**四边 live 已验证** | — |
| `TOKENKEY_FEISHU_WEBHOOK_*` | 有（deploy sync） | **uk1/uk2/us4/us5 已从 LS 拷到 HZ**（不进 git）；新机仍需 post-boot 拷贝 | 告警依赖这两行 |
| `TOKENKEY_IMAGE_BLUE/GREEN` | 有 | 无 | 随蓝绿 |
| Redis / 调度内存态 | 独立 | 独立（不迁） | 限流/窗口态不共享属预期 |
| gemini-web 会话 | 部分 active | 常因 **出口 IP 变更** Session paused | 需 re-import，不是漏搬行 |
| TLS fingerprint / proxy 等 host-local | LS | 迁库时 `proxy_id` 等会重置 | 按边复查 |
| prod stub `schedulable` | — | `cc-*` / `gemini-uk*` / `grok-*` 实测保持 `f`（边侧无健康 anthropic/grok 池或 gemini 会话 paused） | 影响经 prod 选号；修好边侧供应并冒烟 200 后再开 |

切流验收除库行数外，应补：disk-metrics + Feishu webhook、ghcr prune timer、gemini re-import、正式路径 smoke（非 gemini 类）、prod stub `base_url` scheme、prod 容器 DNS。

## 正式 DNS 切流（edge）

1. 按上节完成 **冻写 → 新鲜 dump/restore → 对账**（未切边强制）。  
2. Porkbun：`api-<edge>.tokenkey.dev` A → Hetzner IP（Lightsail IP 先留着）。  
3. 主机 `/var/lib/tokenkey/.env`：  
   `API_DOMAIN="api-<edge>.tokenkey.dev, api-<edge>-hz.tokenkey.dev"`  
   （**必须加引号**）。  
4. `bash ops/stage0/sync_caddyfile_via_ssm.sh edge <mi-…>`（**不设** `EDGE_ID`）→ ACME。  
5. 外网 health 200 + 非 gemini 账号类 smoke → E5 短 soak。  
6. prod 若 NSS 缓存旧 A：临时 `/etc/hosts` pin；TTL 干净后删。

回滚：A 指回 Lightsail；双域名 Caddy 可留。

**探针：** 勿对 Hetzner 用 `run-probe --target edge:<id>`（会解析到 Lightsail）。直投 Hybrid `mi-*`；`sync_caddyfile` **不要**设 `EDGE_ID=`。

## Prod staging

**做：** CFN Hybrid `tokenkey-hetzner-ssm-hybrid-prod` → L0 → `--confirm-paid` 建 Volume + `cax21` → staging DNS `api-hz.tokenkey.dev` → E0/E1。  
**不做（直到全 edge 稳）：** 正式 `api.tokenkey.dev`、P1 dump/restore、冻写窗、`deployable=true`。

```bash
python3 deploy/hetzner/resolve-prod-hetzner-target.py --allow-planned
bash deploy/hetzner/render-prod-bootstrap.sh --check
bash deploy/hetzner/provision-prod.sh --allow-planned
bash deploy/hetzner/provision-prod.sh --allow-planned --confirm-paid --tag X.Y.Z
```

user-data **必须以 `#!/bin/bash` 开头**；AWS CLI 走 awscliv2 zip。  
prod 正式切流：P1–P4/P6（precious+必要日志）→ 冻写 ≤5 min → DNS → P5；写后禁裸 DNS 回旧库。

## 后续 backlog

1. **禁止**再对已切四边整库覆盖 live HZ；LS ≥7d 后可退役实例。  
2. **us3**：配额允许 → 重建 HZ（全新换机）→ 冻写 → precious+logs → 正式 A → LS 停写。  
3. **us6**：同 us3。  
4. **prod**（全 edge 含 us3/us6 稳后）：Volume + P1–P4/P6 → 冻写 ≤5 min → `api.tokenkey.dev` → P5；注意容器 `extra_hosts` / stub `https://`。  
5. gemini-web：各 HZ 边会话 re-import；再评估 `gemini-uk*` stub。  
6. 边侧供应：us4 anthropic 失效号、us4/us5 grok 选号/空池、uk 边补 CC 池——冒烟 200 后再开对应 prod stub。  
7. 新机 Feishu：仍需 post-boot 从匹配 LS `.env` 拷贝（不进 git）。  
8. bootstrap / CFN：默认 `TOKENKEY_PGDUMP_S3_URI` + pgdump timer（减少手工）。

## 硬门禁

- `location=fsn1` · `server_type=cax21` · `architecture=arm`
- prod：`volume_mount=/var/lib/tokenkey` · `volume_size_gb>=40`
- 矩阵 / prod target：`deployable=false` 直至 Phase 门禁绿

```bash
python3 -m unittest deploy/hetzner/test_resolve_edge_hetzner_target.py
python3 -m unittest deploy/hetzner/test_resolve_prod_hetzner_target.py
python3 -m unittest deploy/hetzner/test_render_prod_bootstrap.py
bash deploy/hetzner/render-bootstrap.sh --check
bash deploy/hetzner/render-prod-bootstrap.sh --check
```
