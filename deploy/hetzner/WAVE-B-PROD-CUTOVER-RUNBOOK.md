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
```

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

## B2 — 新鲜 precious dump → HZ restore

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
# 两侧 count：accounts / usage_billing_dedup / usage_logs
# accounts 必须相等；dedup 允许 dump 后极小漂移仅在未冻干净时出现——冻写后应一致；
# usage_logs 在 precious 路径可为 0（预期）。
```

---

## B4 — 正式 prod Caddy（CallModel + apex）

**勿**在 Wave A 对 staging 推正式模板。切流当秒：

```bash
# 渲染校验（本地 dry）：deploy/aws/stage0/render-prod-caddyfile.sh
# Hybrid 注册在 eu-west-2 — 必须在 sync 命令前 export
export AWS_REGION=eu-west-2
API_DOMAIN=api.tokenkey.dev \
GLOBAL_SITE_DOMAIN=tokenkey.dev \
GLOBAL_SITE_PHASE=live \
API_ALIAS_DOMAIN=api.callmodel.io \
ACME_EMAIL=<ops> \
  bash ops/stage0/sync_caddyfile_via_ssm.sh prod mi-033c9569c7fb8b884 "wave-b-prod-caddy"
```

---

## B5 — Porkbun 一次切四 hostname

| Hostname | 新 A |
|---|---|
| `api.tokenkey.dev` | `167.233.211.115` |
| `api.callmodel.io` | 同左 |
| `callmodel.io` | 同左 |
| `tokenkey.dev` | 同左 |

**不动** `status.tokenkey.dev`（Better Stack CNAME）。  
`dig +short` 四条齐 → 继续；不齐 → 不进 B6。

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
