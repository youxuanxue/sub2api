# TokenKey Edge / Prod on Hetzner Cloud

审批基线：[`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)

## 当前状态（2026-10-10；us5/uk2 正式 A 已切 HZ）

| edge | 正式 A（`api-<id>.tokenkey.dev`） | Hetzner IP | Lightsail IP（回滚） | 库复刻 |
|------|-----------------------------------|------------|----------------------|--------|
| **uk1** | **已切** → `188.245.112.147` | 同左 | `18.135.0.171`（app 已停写，PG/Caddy 保留 ≥7d） | precious + logs 已灌；水位对齐后**无需补漏** |
| **us4** | **已切** → `188.245.14.132` | 同左 | `32.188.80.151`（app 已停写，PG/Caddy 保留 ≥7d） | precious + logs 已灌；已定点补漏 42×ulog+42×dedup（≈$0.62） |
| **us5** | **已切** → `91.98.83.56`（冻写重灌后切） | 同左 | `16.144.175.131`（app 已停写，PG/Caddy 保留 ≥7d） | 2026-10-10 `T101255Z`+`logs-T101315Z` |
| **uk2** | **已切** → `2.31.27.73`（冻写重灌后切） | 同左 | `51.24.28.148`（app 已停写，PG/Caddy 保留 ≥7d） | 2026-10-10 `T101627Z`+`logs-T101629Z` |
| **us3** | LS `18.216.113.132`（仍在跑，已无 prod stub） | **无机器**（不再重建） | — | **2026-10-11 账号级退役**：12 账号 → uk1，stub 全切/软删；见「us3/us6 账号级退役」 |
| **us6** | LS `3.147.98.112`（仍在跑，已无 prod stub） | **未起**（不再 provision） | — | **2026-10-11 账号级退役**：19 账号 → uk2，stub 全切/软删；同上 |
| **prod** | 仍 AWS Stage0（正式） | **HZ staging 已绿** `167.233.211.115` · `mi-033c9569c7fb8b884` · Volume `tokenkey-prod-data` · `api-hz` E1 | — | Wave A 齐；dump 已刷 `T155229Z`；Wave B 见 [`WAVE-B-PROD-CUTOVER-RUNBOOK.md`](WAVE-B-PROD-CUTOVER-RUNBOOK.md)；正式 DNS/冻写未做 |

Lightsail ≥7 天保留作回滚；禁止双写业务库。Redis 不迁（可重建）。

**本质：** 每条边都是 **全新换机**（新 cax21 + 新库 + 新出口 IP），不是 Lightsail 原地升级。决策锁在审批基线「本质：全新换机」节。

**编排入口（Agent）：** [`.cursor/skills/tokenkey-host-replacement/SKILL.md`](../../.cursor/skills/tokenkey-host-replacement/SKILL.md) — plan → provision → replicate → cutover → drain → verify；下次 us3/us6/prod 换机先加载该 skill，坑位回写 skill + 本节。

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
**禁止** `TRUNCATE … CASCADE` 做 Mode C OLTP 刷新：会经 FK 清空 `usage_logs*`（Mode C 不回灌）。只用 `wave_b_freeze_delta.py` 的 `session_replication_role=replica` + 无 CASCADE `TRUNCATE`（见 Wave B runbook Mode C）。

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

## Prod staging → 正式切流

**顺序 override（2026-10-10）：** 四边已切后 **先做 prod**；**us3/us6 明确延期**（本轮不触碰）。覆盖审批基线「全 edge 后再 prod」；执行以本节为准。

### Wave A — staging（零用户影响）

**做：** CFN Hybrid `tokenkey-hetzner-ssm-hybrid-prod` → L0 → `--confirm-paid` 建 Volume + `cax21` → 仅 `api-hz.tokenkey.dev` A → E0/E1 → S3 `prod/pgdump` 演练 restore（隔离库）→ 主机 timer 验收。  
**不做：** 正式四 hostname DNS、冻写、live `QA_BUNDLE_*`、改 Edge `remote_ip`、`deployable=true`。

```bash
python3 deploy/hetzner/resolve-prod-hetzner-target.py --allow-planned
bash deploy/hetzner/render-prod-bootstrap.sh --check
bash deploy/hetzner/provision-prod.sh --allow-planned
# paid（推荐 GHA OIDC：本地 Tech-Partner 无 iam:PassRole）:
gh workflow run deploy-prod-hetzner-stage0.yml \
  -f operation=provision -f confirm_instance=tokenkey-prod-hz-cax21 \
  -f allow_planned=true -f confirm_paid=true -f tag=X.Y.Z
# 或本地（需 PassRole）:
ACME_EMAIL=… MAIN_GATEWAY_ALLOWED_CIDR=34.194.234.88/32 GHCR_OWNER=youxuanxue \
GHCR_PAT_SSM_NAME=/tokenkey/ghcr/pat \
bash deploy/hetzner/provision-prod.sh --allow-planned --confirm-paid --tag X.Y.Z
```

Bootstrap（`render-prod-bootstrap.sh`）已嵌入：`tokenkey-pgdump.timer`（`TOKENKEY_PGDUMP_S3_URI=s3://tokenkey-prod-pgdump-<acct>/prod/pgdump`）、`tokenkey-disk-metrics.timer`、`tokenkey-ghcr-prune-daily.timer`；staging Caddy 仍为 `Caddyfile.edge`；`QA_CAPTURE_ENABLED=false`。Feishu webhook 仍 post-boot 从 AWS prod `.env` 拷贝（不进 git）。

**Wave A 实测（2026-10-10）：**
- 点火：GHA `deploy-prod-hetzner-stage0.yml` · tag `1.8.283` · IP `167.233.211.115` · `mi-033c9569c7fb8b884` · E0 `aarch64` · timers active · Feishu webhook 已从 AWS 拷贝  
- 演练 restore（刷新）：`tokenkey-20261010T155229Z.sql.gz` → HZ；对账 `accounts=215=215`；`usage_billing_dedup` HZ `14571497`（dump 窗）；`usage_logs` HZ `0`（precious 预期）；下载 ~17s / restore ~5.0 min  
- E1：`api-hz.tokenkey.dev` A → `167.233.211.115`；LE 证书已签；`https://api-hz.tokenkey.dev/health` → 200  
- 零影响预热：HZ SSM secrets 已同步；`warm_pull` `1.8.283` 绿；控制面默认仍 `i-*`；正式 Caddy dry 四 vhost；**CallModel 公网 NS 已跟齐 Porkbun**（A=`34.194.234.88`）；**Mode C 压窗**见 runbook（C-lite 约 1.5–2.5 min / C-full 约 4–8 min；整库回退约 10–15 min）  
- **Wave B 延期（2026-10-10）：** 现网请求量高，**不做冻写/正式 DNS**；正式流量继续 AWS EIP `34.194.234.88`。低流量窗再批「批准冻写切流」。**Porkbun TTL→300 已 apply**（四正式 A；`api-hz` 仍 600）；Better Stack `api-hz` 旁路仍可人工建。  

user-data **必须以 `#!/bin/bash` 开头**；AWS CLI 走 awscliv2 zip。

### Wave B — 正式切流清单（第二道批准；仍须冻写）

**可照抄 runbook（含 Mode C 压窗、回滚、QA/告警附录）：** [`WAVE-B-PROD-CUTOVER-RUNBOOK.md`](WAVE-B-PROD-CUTOVER-RUNBOOK.md)

**默认 Mode C（压窗）：** 开钟前整库预灌 dump A + watermark → 冻写 → `decide-path`：**C-lite**（OLTP count 齐则只灌 dedup，约 **1.5–2.5 min**）或 **C-full**（OLTP 小包 + dedup，约 **4–8 min**）→ 对账 → DNS。禁止先切 DNS 再补库。SQL/估时：`ops/stage0/wave_b_freeze_delta.py`。

| 步 | 动作 | 红灯 |
|---|---|---|
| B0 / C0 | staging 绿；**预灌 dump A + watermark**；timer/Caddy/CIDR/QA 脚本就绪 | 缺口未关门 |
| B1 / C1 | 冻写 AWS prod，开钟 | — |
| C2（默认） | OLTP truncate+refresh + dedup 增量；失败则解冻、**不切任何正式 A** | 对账失败 |
| B2（回退） | 新鲜 precious 整库 DROP/CREATE restore | restore/对账失败 |
| B3 | 对账 `users`/`accounts`/`api_keys`/`groups`/`settings`/`usage_billing_dedup` | 不一致 |
| B4 | `sync_caddyfile_via_ssm.sh prod <mi-*>` 推正式 prod Caddy（CallModel + apex + `@machine`） | Caddy/ACME 红 |
| B5 | Porkbun **一次**切四 hostname → 新 IP（`status.tokenkey.dev` 不动；CallModel NS 已回 Porkbun） | dig 未齐 |
| B6 | `EDGE_MAIN_GATEWAY_ALLOWED_CIDR=<新IP>/32` + 全 deployable edge `sync_caddyfile`；解冻写到 HZ | Edge 403 |
| B7 | 注入 live `QA_BUNDLE_*`（勿在 Wave A 做）；sync QA maintenance timer；canary | canary 红 |
| B8 | Feishu disk-metrics 绿；Better Stack `api.tokenkey.dev/health`；静音旧 EC2 CW 盘/CPU | 假阳性 |
| B9 | P5：60min 错率 ≤ 基线+2pp；控制面目标 `i-*`→`mi-*`（`resolve_prod_ssm_target.py` / SSM param）；AWS 停 app，PG/Caddy ≥7d standby | P5 红 → 写前可回 A |

IdP / 支付 webhook **URL 字符串不变**（仍 `https://api.tokenkey.dev/...`）。写后禁裸 DNS 回旧库。

控制面默认仍解析 AWS `i-*`；切后才 `PROD_SSM_TARGET=hetzner` 或 PutParameter `/tokenkey/prod/control-plane-ssm-target=hetzner`。

## us3/us6 账号级退役（2026-10-11 执行）

**与审批基线的差异（已获用户逐项确认）：** 基线 [`hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md) §节奏/§路线锁的是
「us3/us6 重建 HZ、逻辑 edge id 不变、整库复刻」。实际改为**账号级退役**：把两边账号折叠进既有的
uk1/uk2，edge id 不再保留，**不重建机器、不做整库复刻**。基线文档保留原决策不改（人工审批产物）。

**为何不违反「禁止只靠 migrate-edge-accounts 当全量迁移」：** 那条禁令（基线 §30/§47、
[`README`](README.md) 的「数据复刻（必做）」节）约束的是「换机但要保住整台边」的场景——那需要
`usage_logs*`/dedup/settings/audit 全量。这里不保留 edge id，历史数据留在原机只读（≥7d），
所以脚本的设计用途（搬无法从 admin UI 重录的凭证）正好吻合。

### 实际路径（每类平台五步，主力最后动）

1. `migrate-edge-accounts.py extract → build`（dry-run，逐条审 SQL）→ `load --execute`
2. 目标边建缺失的 relay `api_key`（`api_keys.group_id` 必填，所以先有组才有 key）
3. 把 `*-us3`/`*-us6` 后缀组合并进目标边原有同名组，删空组
4. 改 prod stub 的 `credentials.base_url` + `api_key` + `name`（**分组绑定/优先级/is_exclusive 一律不动**）
5. 从 prod 实测 `/v1/models` + 盯盘确认错误零新增，再软删剩余 stub

### 结果

| 项 | us3 → uk1 | us6 → uk2 |
|---|---|---|
| 账号 | 12/12（`name\|platform\|cred_len` 逐字节对账，差集为空） | 19/19（同法对账） |
| 切流 stub | `openai-us3`→`openai-uk1`(64)、`china-us3`→`china-uk1`(198) | `openai-us6`→`openai-uk2`(63)、`china-us6`→`china-uk2`(199)、`kiro-us6`→`kiro-uk2`(66) |
| 软删 stub | `cc-us3`(52)、`kiro-us3`(70)、`antigravity-us3`(61)、`gemini-us3`(208)；`grok-us3`(80) 早前已删 | `cc-us6`(55)、`grok-us6`(81)、`antigravity-us6`(85)、`gemini-us6`(210) |
| 合并后组 | `antigravity` 7、`gemini-web` 2、`default` 1、`china` 3、`openai` 4 | `antigravity` 9、`gemini-web` 5、`kiro` 5、`china` 7、`openai` 4 |
| 新建 relay key | `relay-openai-uk1`、`relay-china-uk1` | `relay-openai-uk2`、`relay-china-uk2`、`relay-kiro-uk2` |

`schedulable`/`status` 用 `--preserve-schedulable` 保持与源侧一致（含故意保留 `anti-503`/`anti-510` 的
`error`）；唯一刻意偏离是 gemini —— 全部压成 `error`+不可调度等 re-import。

### 坑位（复用时先读）

- **空池废 stub**：`kiro-us3`(70)、`grok-us6`(81) 的 prod stub 活着但边库对应平台 0 账号。迁移前要用
  「prod stub ↔ edge 平台账号数」对照表筛一遍，否则会为空池白做一轮。
- **孤儿账号**：uk2 的 `nvidia-build-492/493/505` 此前 live 但**未绑任何组**，根本路由不到（非本次引入）。
  已收养进 `china`。目标边迁入前先跑一次 orphan 检查。
- **配置不随账号走**：账号搬完后 `settings` 仍是目标边的。本次实际漏过一项——us3 独有的
  `tk_account_model_mapping_runtime`（antigravity `gemini-3-flash`→`gemini-3.8-flash-medium`），
  靠事后复审才抓到；不补就是静默行为漂移。另外 `ops_advanced_settings`/`ops_metric_thresholds`
  是舰队标准值（us3/us4/us5/us6 md5 全等），**uk1/uk2 切流时就漏配了**，本次一并补齐六边一致。
- **gemini-web 会话必失效,kiro OAuth 不受影响**：uk2 原有两个带会话的 gemini 迁来后即 `error`，
  是出口 IP 变更的实证；而 kiro 5 个 OAuth 搬到 uk2 后**立刻成功服务**（01:40:13 起），
  所以 kiro 凭证不绑出口 IP。迁 gemini 必须排 re-import，迁 kiro 不必。
- **脚本不幂等**：裸 `INSERT ... RETURNING id`，无 `ON CONFLICT`，重跑即重复账号且无 undo。
  每批只跑一次 `load --execute`；`--replace-target` 全程禁用（会软删目标所有账号 = 清空 live 边）。
- **`account_groups` 只有 4 列**（`account_id`/`group_id`/`priority`/`created_at`，无 `updated_at`）；
  `settings` 只有 4 列（`id`/`key`/`value` text/`updated_at`，无 `created_at`）；边机**无 pgcrypto**
  （`gen_random_bytes` 不可用，relay key 用 `md5(random()||clock_timestamp())` 拼 64 hex）。
- **改 stub `base_url` 是瞬时切换、无灰度**，但也正因此是唯一的快速回滚手段（改回去即可）。
  切前必须先从 prod 侧实测目标边 `/v1/models` 拿到 200。
- 软删有流量的 stub 前，确认它所在的每个组还剩 ≥2 个可调度同伴（本次用 SQL 守卫强制）。

### 剩余（未做）

停写 us3/us6 **故意延后**：uk1/uk2 的 gemini 会话还没 re-import，而 `gemini-web-498`(us3)/
`gemini-web-506`(us6) 的原会话在原机上，是唯一退路。re-import 落地后再按纪律收尾：
停 app + gemini-web → `.env` 置 `TOKENKEY_LS_STANDBY_READONLY=1` → 保留 PG/Caddy ≥7d →
`edge-targets-lightsail.json` 翻 `deployable=false` → ≥7d 后删实例（参照 2026-06-23
uk1旧/us2/us7 退役清单，含**删 prod mirror account**）。

## 后续 backlog

1. **禁止**再对已切四边整库覆盖 live HZ；LS ≥7d 后可退役实例。  
2. **prod Wave A/B**（当前刀）：见上节。
3. ~~us3 重建 HZ~~ **已取消**：2026-10-11 改走账号级退役（账号迁 uk1），不再重建机器。剩停写 + 实例退役，见下节。
4. ~~us6 重建 HZ~~ **已取消**：同 us3（账号迁 uk2）。
5. gemini-web：**7 个账号待运营 re-import**（uk1 `gemini-web-498`/`gemini-web`；uk2 `gemini-web-506`/`510`/`492`/`505`/`493`），全部 `status=error`+`schedulable=false` 并带原因；re-import 后开边侧账号 + prod `gemini-uk1`/`uk2` 即通（key 已逐字节核对一致）。**期间 prod gemini 组仅 `gemini-us4`/`us5` 两个可调度 stub 承载，属单点。**  
6. 边侧供应：us4 anthropic 失效号、us4/us5 grok 选号/空池、uk 边补 CC 池——冒烟 200 后再开对应 prod stub。  
7. 新机 Feishu：仍需 post-boot 从 AWS prod `.env` 拷贝（不进 git）。  
8. Wave B 后：prod 发版 workflow 解析 Hybrid `mi-*`；蓝绿 vs 单色路径对齐。

## 硬门禁

- `location=fsn1` · `server_type=cax21` · `architecture=arm`
- prod：`volume_mount=/var/lib/tokenkey` · `volume_size_gb>=40`
- 矩阵：`uk1/uk2/us4/us5` 已 `deployable=true`（正式切流后）；`us3/us6` 在 Lightsail 矩阵中仍 `deployable=true`（机器在跑，可收 probe/运维下发），**停写退役时才翻 `false`**；prod target 仍 `false` 直至切流

```bash
python3 -m unittest deploy/hetzner/test_resolve_edge_hetzner_target.py
python3 -m unittest deploy/hetzner/test_resolve_prod_hetzner_target.py
python3 -m unittest deploy/hetzner/test_render_prod_bootstrap.py
bash deploy/hetzner/render-bootstrap.sh --check
bash deploy/hetzner/render-prod-bootstrap.sh --check
```
