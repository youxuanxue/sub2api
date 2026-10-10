# Wave B — Prod → Hetzner 正式切流（可照抄）

**门禁：** 仅在人工明确批准「冻写切流」后执行。高流量窗默认延期。  
**禁止提前做：** 冻写、切四正式 hostname、改 Edge `MAIN_GATEWAY_ALLOWED_CIDR`、staging 挂 live `QA_BUNDLE_*`、静音仍在服务的 AWS CloudWatch。

| 常量 | 值（2026-10-10 Wave A） |
|---|---|
| HZ IP | `167.233.211.115` |
| SSM Hybrid | `mi-033c9569c7fb8b884`（`eu-west-2`） |
| Staging | `https://api-hz.tokenkey.dev` |
| AWS EIP（回滚 A） | `34.194.234.88` |
| AWS Instance | CFN `tokenkey-prod-stage0` → `i-*`（`us-east-1`） |
| Image tag（例） | 运行时绿标（勿写死；切前用 resolver 读） |

控制面解析（默认仍 AWS，切后才翻）：

```bash
# 默认 → i-* / us-east-1
python3 ops/stage0/resolve_prod_ssm_target.py
# 切后：PutParameter 或一次性
PROD_SSM_TARGET=hetzner python3 ops/stage0/resolve_prod_ssm_target.py
```

---

## B0 — 开钟前核对（可提前反复做）

```bash
curl -fsS -o /dev/null -w '%{http_code}\n' https://api-hz.tokenkey.dev/health   # 200
python3 ops/stage0/resolve_prod_ssm_target.py --target aws   # 仍为 i-*
python3 deploy/hetzner/resolve-prod-hetzner-target.py --allow-planned
# timers on HZ
AWS_REGION=eu-west-2 aws ssm send-command --instance-ids mi-033c9569c7fb8b884 \
  --document-name AWS-RunShellScript \
  --parameters 'commands=["systemctl is-active tokenkey-pgdump.timer tokenkey-disk-metrics.timer tokenkey-ghcr-prune-daily.timer"]'
# CallModel 公网 NS 须已是 Porkbun（改回后递归缓存可能滞后）
dig +short NS callmodel.io @8.8.8.8   # *.ns.porkbun.com
dig +short A callmodel.io api.callmodel.io @8.8.8.8   # 34.194.234.88
```

### B0b — Porkbun TTL 预压（可提前；API）

```bash
# 凭证：PORKBUN_API_KEY / PORKBUN_SECRET_API_KEY（本地 env，不进 git）
# ipv4 "-" = 只改 TTL，保留现有 A（切流后误跑也不会写回旧 EIP）
for spec in \
  'api.tokenkey.dev:tokenkey.dev' \
  'tokenkey.dev:tokenkey.dev' \
  'api.callmodel.io:callmodel.io' \
  'callmodel.io:callmodel.io'
do
  host="${spec%%:*}"; domain="${spec##*:}"
  bash ops/dns/porkbun-upsert-a.sh "$host" - --domain "$domain" --ttl 300 --apply
done
# 不动 status.tokenkey.dev / api-hz
```

切 A 后残余缓存最多约一个 TTL（300s）；预压只缩短排空，不改指向。

### B0c — Better Stack `api-hz` 旁路（人工；可提前）

见附录 B。勿替换正式 `api.tokenkey.dev` monitor。

### 冻写窗耗时

| 模式 | 用户可感知写入中断 | 何时用 |
|---|---|---|
| **Mode C（默认，压窗）** | **约 4–8 min**（预灌后；见下） | HZ 已预灌 dump A + watermark |
| Mode B（整库重灌） | 约 10–15 min | Mode C 红灯回退；或预灌失效 |

Mode B 分步实测（2026-10-10）：冻写 0.5–1 + dump 1–1.5 + 下载 0.5–1 + DROP/CREATE restore ~5 + 对账/Caddy/DNS/CIDR ~4–8。  
瓶颈是 `usage_billing_dedup`（~14M 行 / 盘上 ~4GB），不是小表。  
DNS TTL 残余另计（预压 300s 后最多约 5 min 部分客户端仍打已冻 AWS）。

---

## Mode C — 预灌 + 冻写短窗增量（推荐）

**仍必须冻写。** 禁止「先切 DNS 再补库」。压缩的是冻写窗内的 **DB 路径**，不是取消冻写。

### C0 — 开钟前（零用户影响，可反复）

1. AWS 打一轮 precious dump → S3（或不另打、用最新 `tokenkey-*.sql.gz`）。  
2. HZ：整库 DROP/CREATE restore 该 dump（与 Wave A 演练相同）；`api-hz` 恢复 200。  
3. 在 **HZ** 上抓 watermark（写入本地文件，勿提交 git）：

```bash
python3 ops/stage0/wave_b_freeze_delta.py print-watermark-sql
# 在 HZ postgres 执行上述 SQL，保存 JSON，例如：
# {"dedup_max_id":14615286,"dedup_count":14571497,"accounts":215,...}
```

4. 列出 OLTP 刷新表（排除 logs + `usage_billing_dedup`）：

```bash
python3 ops/stage0/wave_b_freeze_delta.py print-list-oltp-sql
# 在任一侧 postgres 执行，得到逗号分隔表名
python3 ops/stage0/wave_b_freeze_delta.py plan \
  --watermark-json "$WM_JSON" \
  --oltp-tables "$OLTP_TABLES"
# 估时：python3 ops/stage0/wave_b_freeze_delta.py estimate --dedup-delta-rows <AWS_count - HZ_count>
```

预灌越新鲜，冻写窗内 dedup 增量越小（小时级预灌常见数千行，秒～数十秒可灌完）。

### C1 — 冻写 AWS（开钟）

同下节 B1（stop app；PG/Caddy 保留）。

### C2 — 冻写窗内：OLTP 全量小包 + dedup 增量（勿 DROP DATABASE）

**禁止** `TRUNCATE … CASCADE`（会经 FK 清空 `usage_logs*` 等 Mode C 不回灌的表）。截断 SQL 必须来自 `plan` 的 `sql.truncate_oltp`（`session_replication_role=replica` + 无 CASCADE）。

Exclude 旗标与表清单同一 SSOT（勿手写 glob）：

```bash
python3 ops/stage0/wave_b_freeze_delta.py print-pg-dump-exclude-args
# → --exclude-table-data=usage_logs* … --exclude-table-data=usage_billing_dedup
```

在 **已冻写的 AWS**（`$IID`）上（示意；容器名以主机为准）：

```bash
EXCL=$(python3 ops/stage0/wave_b_freeze_delta.py print-pg-dump-exclude-args)
# pg_dump data-only → 本地/S3 工件（示例）
# docker exec "$PG" pg_dump -U tokenkey -d tokenkey --data-only $EXCL | gzip > /tmp/wave-b-oltp.sql.gz
# dedup CSV：plan.sql.dedup_delta_copy | docker exec -i "$PG" psql … -c "COPY …" → /tmp/wave-b-dedup.csv
```

在 **HZ** 上：

1. stop `tokenkey`（勿动 postgres）。  
2. 执行 `plan.sql.truncate_oltp`（replica role，**无** CASCADE）→ `gunzip -c wave-b-oltp.sql.gz | psql`。  
3. `plan.sql.dedup_delta_apply`：CSV → temp → `INSERT … ON CONFLICT (request_id, api_key_id) DO NOTHING`，并校正 sequence。  
4. 对账：`plan.sql.reconcile` — `users` / `accounts` / `api_keys` / `groups` / `settings` / `usage_billing_dedup` **count 与冻写后 AWS 一致**。  

红灯 → **解冻 AWS，中止**；不切正式 A。  
回退整库：改走 Mode B（下节 B2）。

### C3 — 对账通过后

继续 B4 Caddy → B5 DNS → B6 Edge CIDR（与 Mode B 相同）。  
切后 **不要**再从 AWS 整库覆盖 HZ；历史 logs 可选后续 append-only 补，不挡切流。

---

## B1 — 冻写 AWS prod（开钟）

在 AWS `i-*` 上停业务写入（保留 PG/Caddy ≥7d standby）：

```bash
IID=$(python3 ops/stage0/resolve_prod_ssm_target.py --target aws --format instance-id)
# 停 app 容器（blue/green 活动色）；勿删数据卷
AWS_REGION=us-east-1 aws ssm send-command --instance-ids "$IID" \
  --document-name AWS-RunShellScript \
  --comment "wave-b-freeze-write" \
  --parameters 'commands=["cd /var/lib/tokenkey && docker compose --env-file .env stop tokenkey tokenkey-blue tokenkey-green 2>/dev/null || true; docker ps --format {{.Names}} | grep -E \"^tokenkey\" || echo APP_STOPPED"]'
```

---

## B2 — Mode B 回退：新鲜 precious 整库 → HZ restore

仅当 Mode C 不可用（无预灌 / 增量对账红）时使用。

```bash
# AWS：跑一轮 pgdump（precious）
AWS_REGION=us-east-1 aws ssm send-command --instance-ids "$IID" \
  --document-name AWS-RunShellScript --timeout-seconds 900 \
  --parameters 'commands=["sudo /usr/local/bin/tokenkey-pgdump.sh"]'
# 等 S3 出现新对象后：
LATEST=$(aws s3 ls s3://tokenkey-prod-pgdump-682751977094/prod/pgdump/ | awk '{print $4}' | tail -1)
MI=mi-033c9569c7fb8b884
# 下载 + DROP/CREATE + gunzip|psql + start app（见 README 演练脚本；失败则解冻、不切任何正式 A）
```

红灯：restore/对账失败 → **解冻 AWS app，中止**。

---

## B3 — 对账

```bash
# Mode C / B：users / accounts / api_keys / groups / settings / usage_billing_dedup
# 冻写后两侧 count 必须一致；usage_logs 在 precious / Mode C 路径可为 0（预期）。
```

---

## B4 — 正式 prod Caddy（CallModel + apex）

**勿**在 Wave A 对 staging 推正式模板。切流当秒：

```bash
# 渲染校验（本地 dry）：deploy/aws/stage0/render-prod-caddyfile.sh
# Hybrid 注册在 eu-west-2 — 必须在 sync 命令前 export
export AWS_REGION=eu-west-2
API_DOMAIN=api.tokenkey.dev \
SITE_DOMAIN=tokenkey.dev \
GLOBAL_SITE_DOMAIN=callmodel.io \
GLOBAL_SITE_PHASE=live \
API_ALIAS_DOMAIN=api.callmodel.io \
ACME_EMAIL=<ops> \
  bash ops/stage0/sync_caddyfile_via_ssm.sh prod mi-033c9569c7fb8b884 "wave-b-prod-caddy"
```

本地 dry（切前可反复跑，不触主机）：

```bash
API_DOMAIN=api.tokenkey.dev SITE_DOMAIN=tokenkey.dev \
GLOBAL_SITE_DOMAIN=callmodel.io GLOBAL_SITE_PHASE=live \
API_ALIAS_DOMAIN=api.callmodel.io ACME_EMAIL=<ops> \
  bash deploy/aws/stage0/render-prod-caddyfile.sh \
    deploy/aws/stage0/Caddyfile /tmp/prod-caddy.dry
# 应含 tokenkey.dev / callmodel.io / api.tokenkey.dev / api.callmodel.io 四 vhost
```

---

## B5 — Porkbun 一次切四 hostname

| Hostname | 新 A | DNS（2026-10-10） |
|---|---|---|
| `api.tokenkey.dev` | `167.233.211.115` | Porkbun |
| `tokenkey.dev` | 同左 | Porkbun |
| `api.callmodel.io` | 同左 | Porkbun（已从 Cloudflare NS 改回；权威 A 已是 AWS EIP） |
| `callmodel.io` | 同左 | 同左 |

**不动** `status.tokenkey.dev`（Better Stack CNAME）。  
切前确认公网 `dig NS callmodel.io` 已是 `*.ns.porkbun.com`（部分递归缓存可能短暂仍报 Cloudflare）。  
`dig +short @8.8.8.8` 四条 A 均到新 IP → 继续；不齐 → 不进 B6。

回滚（仅写前/写后极短窗、且 DB 未双写）：四条 A 改回 `34.194.234.88`。

---

## B6 — Edge CIDR + 解冻到 HZ

```bash
# 当秒：全 deployable edge
export EDGE_MAIN_GATEWAY_ALLOWED_CIDR=167.233.211.115/32
# 对每个 deployable edge mi-*：
#   MAIN_GATEWAY_ALLOWED_CIDR=$EDGE_MAIN_GATEWAY_ALLOWED_CIDR \
#   ACME_EMAIL=... bash ops/stage0/sync_caddyfile_via_ssm.sh edge <mi-*> "wave-b-edge-cidr"
# GitHub var EDGE_MAIN_GATEWAY_ALLOWED_CIDR 同步更新，避免下次 edge deploy 写回旧 EIP。

# HZ app 已在 B2 restore 后 running；确认 /health 经正式 hostname 200。
```

提前改 CIDR 会断边回源 —— **禁止**在 B5 前执行。

---

## B7 — QA Bundle（仅切后）

见本文件附录「QA Bundle 清单」。Wave A / staging **禁止**挂 live 队列。

```bash
# 切后示例（值从现网抄，勿提交 git）：
AWS_REGION=eu-west-2 \
QA_BUNDLE_ENABLED=true \
QA_BUNDLE_QUEUE_URL='https://sqs.us-east-1.amazonaws.com/682751977094/tokenkey-prod-qa-bundle' \
QA_BUNDLE_STORAGE_DRIVER=s3 \
QA_BUNDLE_STORAGE_REGION=us-east-1 \
QA_BUNDLE_STORAGE_BUCKET=tokenkey-prod-qa-bundles-682751977094 \
QA_BUNDLE_STORAGE_PREFIX=user-qa \
QA_MAINTENANCE_TIMER_STATE=enabled \
  bash ops/stage0/sync-qa-maintenance-timer-via-ssm.sh mi-033c9569c7fb8b884 "wave-b-qa"
# boundary timer 同理：ops/stage0/sync-qa-boundary-timer-via-ssm.sh
# 再跑 QA canary（deploy-qa-bundle / verify_qa_bundle_infra）
```

---

## B8 — 告警

1. Better Stack：确认正式 monitor 仍盯 `https://api.tokenkey.dev/health`；staging `api-hz` monitor 可保留作旁路。  
2. **静音（切后才做）** 旧 EC2 CloudWatch（先不在延期窗执行）：

| Alarm | Namespace | 动作 |
|---|---|---|
| `tokenkey-prod-cpu-sustained-high` | `AWS/EC2` | `disable-alarm-actions` |
| `tokenkey-prod-data-volume-used` | `tokenkey/EC2` | 同左 |
| `tokenkey-prod-root-volume-used` | `tokenkey/EC2` | 同左 |

```bash
# 切后才跑：
for a in tokenkey-prod-cpu-sustained-high tokenkey-prod-data-volume-used tokenkey-prod-root-volume-used; do
  aws cloudwatch disable-alarm-actions --region us-east-1 --alarm-names "$a"
done
```

3. Feishu `tokenkey-disk-metrics` 在 HZ 上应已绿（Wave A 已拷 webhook）。

---

## B9 — P5 + 控制面翻到 mi-*

```bash
# 60min 错率 ≤ 基线+2pp（运维看板）
# 翻控制面默认目标（一次性，不可逆到「默认 AWS」除非再 PutParameter）：
aws ssm put-parameter --region us-east-1 \
  --name /tokenkey/prod/control-plane-ssm-target \
  --type String --value hetzner --overwrite
python3 ops/stage0/resolve_prod_ssm_target.py   # 应输出 mi-* / eu-west-2
# GitHub var（可选显式）：PROD_SSM_TARGET=hetzner
# 此后 deploy-stage0 走 Hybrid；STAGE0_DEPLOY_PROFILE=prod（mi-* 强制 prod 配置面）
# AWS：保持 PG/Caddy ≥7d；app 保持停写
```

---

## 回滚摘要

| 阶段 | 动作 |
|---|---|
| B2/B3 红 | 解冻 AWS app；不切 DNS |
| B5 后、双写前 | 四 A → `34.194.234.88`；Edge CIDR 改回旧 EIP/32 |
| 写后长时间 | **禁止**裸 DNS 回旧库（分叉）；走 DR / 正向修复 |

---

## 附录 A — QA Bundle 清单（从现网抄齐；勿挂 staging）

现网 AWS prod `.env` 键（2026-10-10 盘点，值不入库）：

| Key | 现网形态 |
|---|---|
| `QA_BUNDLE_ENABLED` | `true` |
| `QA_BUNDLE_QUEUE_URL` | `…/tokenkey-prod-qa-bundle`（账户 `682751977094`） |
| `QA_BUNDLE_STORAGE_DRIVER` | `s3` |
| `QA_BUNDLE_STORAGE_REGION` | `us-east-1` |
| `QA_BUNDLE_STORAGE_BUCKET` | `tokenkey-prod-qa-bundles-682751977094` |
| `QA_BUNDLE_STORAGE_PREFIX` | `user-qa` |
| `QA_CAPTURE_EXPORT_*` | bucket `tokenkey-prod-qa-exports-682751977094` |
| `QA_ARCHIVE_*` | bucket `tokenkey-prod-qa-raw-archive-682751977094` |

Timers（AWS）：`tokenkey-qa-maintenance.timer` + `tokenkey-qa-boundary.timer` = enabled/active。

Sync owners：

- `ops/stage0/sync-qa-maintenance-timer-via-ssm.sh`
- `ops/stage0/sync-qa-boundary-timer-via-ssm.sh`
- 校验：`ops/qa/verify_qa_bundle_infra.sh`

**Staging 纪律：** HZ bootstrap 保持 `QA_CAPTURE_ENABLED=false`；Wave B 前不要把 live SQS/bucket 写进 `api-hz`。

---

## 附录 B — Better Stack `api-hz`（旁路，可提前）

UI：Better Stack → Uptime → Create monitor

- URL：`https://api-hz.tokenkey.dev/health`
- 期望：HTTP 200
- 勿替换现有 `https://api.tokenkey.dev/health` monitor
- 公开状态页仍只展示正式 API（见 `ops/stage0/better-stack-status-page.md`）

---

## 附录 C — 密钥 / 镜像保热（可提前）

```bash
# 把 AWS 三件套同步到 HZ **SSM 参数**（需运维凭据；plaintext 不进 shell history 用文件）
SRC=/tokenkey/prod/stage0/env-secrets-backup
DST=/tokenkey/hetzner/prod/stage0/env-secrets-backup
aws ssm get-parameter --region us-east-1 --name "$SRC" --with-decryption \
  --query Parameter.Value --output text > /tmp/tk-env-secrets.env
chmod 600 /tmp/tk-env-secrets.env
aws ssm put-parameter --region eu-west-2 --name "$DST" --type SecureString \
  --value file:///tmp/tk-env-secrets.env --overwrite
shred -u /tmp/tk-env-secrets.env 2>/dev/null || rm -f /tmp/tk-env-secrets.env

# HZ 预热当前绿标（只 pull，不改 .env / 不重启）
TAG=$(AWS_REGION=us-east-1 bash ops/stage0/resolve-prod-running-tag-via-ssm.sh)
AWS_REGION=eu-west-2 bash ops/stage0/warm_pull_via_ssm.sh "$TAG" mi-033c9569c7fb8b884 "wave-a-warm"
```

**禁止在 Wave A staging 把 `POSTGRES_PASSWORD` 写进运行中的 `.env` 后 `compose up`：**  
会 recreate Postgres 容器，与数据卷内旧口令不一致 → app crash。  
Wave B 冻写 restore 时用 `tokenkey-restore-edge-env-secrets.sh` / 与 dump 同窗写入，或先 `ALTER USER` 再切 `.env`。  
SSM 参数预同步是安全的；运行时注入留到 B2。
