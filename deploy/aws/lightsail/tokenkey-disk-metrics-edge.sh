#!/bin/bash
# TokenKey Stage0 EDGE — on-box disk-full Feishu alert (Lightsail variant of the
# prod #778 alert in deploy/aws/stage0/stage0-ec2-bootstrap.sh).
#
# Single source of record: consumed both by
#   - deploy/aws/lightsail/render-bootstrap.sh (bakes it into new-edge user-data), and
#   - ops/stage0/sync-edge-host-units-via-ssm.sh (pushes it onto running edges via SSM).
# Keep those two in sync by editing ONLY this file.
#
# Differences vs the prod EC2 alert (intentional, per "乔布斯审核" — cut to essentials):
#   - df target is /  (edge has NO separate /var/lib/tokenkey data volume; it is
#     a directory on the single root volume).
#   - NO CloudWatch put-metric. Edges have no DataVolumeDiskAlarm and may lack the
#     cloudwatch:PutMetricData IAM, so a metric publish here is a perpetually-failing
#     aws call every 5min — pure noise. The Feishu post is the whole point.
#   - Node label from API_DOMAIN (.env), not IMDS instance-id (Lightsail metadata differs).
#
# A full root volume crashes Postgres AND degrades the app, so this alert MUST run
# independent of Docker/Postgres — the timer fires it every 5min on-box. Webhook +
# secret are injected via /var/lib/tokenkey/.env; absent webhook => silent no-op.
set -euo pipefail

USED="$(df -P / 2>/dev/null | awk 'NR==2 {gsub(/%/,"",$5); print $5}')"
[ -z "${USED}" ] && exit 0

THRESHOLD="${TOKENKEY_DISK_ALERT_THRESHOLD:-85}"
COOLDOWN="${TOKENKEY_DISK_ALERT_COOLDOWN_SEC:-1800}"
STAMP=/run/tokenkey-disk-alert.stamp
WEBHOOK=""; SECRET=""; NODE="$(hostname)"
if [ -r /var/lib/tokenkey/.env ]; then
  WEBHOOK="$(sed -n 's/^TOKENKEY_FEISHU_WEBHOOK_URL=//p' /var/lib/tokenkey/.env | head -1)"
  SECRET="$(sed -n 's/^TOKENKEY_FEISHU_WEBHOOK_SECRET=//p' /var/lib/tokenkey/.env | head -1)"
  DOM="$(sed -n 's/^API_DOMAIN=//p' /var/lib/tokenkey/.env | head -1)"
  [ -n "${DOM}" ] && NODE="${DOM}"
fi

if [ "${USED}" -ge "${THRESHOLD}" ] && [ -n "${WEBHOOK}" ]; then
  NOW="$(date +%s)"
  LAST=0; [ -r "${STAMP}" ] && LAST="$(cat "${STAMP}" 2>/dev/null || echo 0)"
  if [ "$((NOW - LAST))" -ge "${COOLDOWN}" ]; then
    TEXT="🔴 P0 磁盘将满 ${NODE} — 数据盘 / 使用率 ${USED}% (阈值 ${THRESHOLD}%)。Postgres 满盘会崩溃→网关全挂。立即清 docker 镜像/日志或扩容。node=${NODE}"
    # Feishu custom-bot signed message: sign = base64(HMAC-SHA256(key=ts\nsecret, "")).
    if [ -n "${SECRET}" ]; then
      SIGN="$(printf '' | openssl dgst -sha256 -hmac "${NOW}"$'\n'"${SECRET}" -binary 2>/dev/null | base64)"
      PAYLOAD="$(printf '{"timestamp":"%s","sign":"%s","msg_type":"text","content":{"text":"%s"}}' "${NOW}" "${SIGN}" "${TEXT}")"
    else
      PAYLOAD="$(printf '{"msg_type":"text","content":{"text":"%s"}}' "${TEXT}")"
    fi
    # Feishu returns HTTP 200 even when it REJECTS (bad signature / missing keyword);
    # the real status is the body's "code" field (0 = delivered). Stamp the cooldown
    # only on code:0, so a misconfigured webhook keeps retrying every tick instead of
    # silently dropping every alert while the stamp suppresses retries for COOLDOWN.
    RESP="$(curl -sS -m 10 -X POST "${WEBHOOK}" -H 'Content-Type: application/json' -d "${PAYLOAD}" 2>/dev/null || true)"  # preflight-allow: swallow — curl failure ⇒ empty RESP ⇒ no stamp ⇒ retry next tick
    case "${RESP}" in
      *'"code":0'*) echo "${NOW}" > "${STAMP}" || true ;;  # preflight-allow: swallow — best-effort cooldown stamp; alert delivered
    esac
  fi
fi
