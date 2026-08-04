#!/bin/bash
# TokenKey Stage0 EDGE — on-box disk-full + memory-pressure Feishu alerts
# (Lightsail variant of the prod alert in deploy/aws/stage0/stage0-ec2-bootstrap.sh).
#
# Single source of record: consumed by
#   - ops/stage0/sync-edge-host-units-via-ssm.sh (pushes onto running edges via SSM;
#     also called from deploy-edge-lightsail-stage0 after provision/upgrade).
# Edit ONLY this file for edge host-pressure alert logic.
#
# Differences vs the prod EC2 alert (intentional):
#   - df target is /  (edge has NO separate /var/lib/tokenkey data volume).
#   - NO CloudWatch put-metric. Edges have no DataVolumeDiskAlarm and may lack
#     cloudwatch:PutMetricData IAM — Feishu post is the whole point.
#   - Node label from API_DOMAIN (.env), not IMDS instance-id.
#
# These alerts MUST run independent of Docker/Postgres — the timer fires every
# 5min on-box. Webhook + secret via /var/lib/tokenkey/.env; absent webhook =>
# silent no-op. Memory alert is the 2026-08-03 us6 OOM/hang leading indicator
# (prod parity with 2026-06-17 mem-guard).
set -euo pipefail

if [ "${1:-}" = "--selftest" ]; then
  # Pure local checks — no network, no .env required.
  fail=0
  MEMUSEDPCT="$(printf '%s\n' 'MemTotal: 1000 kB' 'MemAvailable: 50 kB' | awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} END{ if(t>0) printf "%d",(t-a)*100/t; else print 0 }')"
  [ "$MEMUSEDPCT" = "95" ] || { echo "FAIL mem pct awk got=$MEMUSEDPCT want=95" >&2; fail=1; }
  SWAPPCT="$(printf '%s\n' 'SwapTotal: 2000 kB' 'SwapFree: 500 kB' | awk '/^SwapTotal:/{t=$2} /^SwapFree:/{f=$2} END{ if(t>0) printf "%d",(t-f)*100/t; else print 0 }')"
  [ "$SWAPPCT" = "75" ] || { echo "FAIL swap pct awk got=$SWAPPCT want=75" >&2; fail=1; }
  USED="$(printf '%s\n' 'Filesystem 1024-blocks Used Available Capacity Mounted' '/dev/root 100 90 10 90% /' | awk 'NR==2 {gsub(/%/,"",$5); print $5}')"
  [ "$USED" = "90" ] || { echo "FAIL df awk got=$USED want=90" >&2; fail=1; }
  THRESHOLD=85
  RECOVER_THRESHOLD=$((THRESHOLD - 5))
  [ "$RECOVER_THRESHOLD" = "80" ] || { echo "FAIL recover default got=$RECOVER_THRESHOLD want=80" >&2; fail=1; }
  # latch: above alert threshold arms; below recover clears
  alert_used=90
  recover_used=75
  should_arm=0
  should_recover=0
  if [ "$alert_used" -ge "$THRESHOLD" ]; then should_arm=1; fi
  if [ "$recover_used" -lt "$RECOVER_THRESHOLD" ]; then should_recover=1; fi
  [ "$should_arm" = "1" ] || { echo "FAIL latch arm" >&2; fail=1; }
  [ "$should_recover" = "1" ] || { echo "FAIL latch recover" >&2; fail=1; }
  # migration: old script wrote cooldown stamp only (no DISK_ACTIVE_STAMP latch)
  legacy_has_latch=0
  legacy_has_cooldown=1
  should_migrate_recover=0
  if [ "${legacy_has_latch}" -eq 0 ] && [ "${legacy_has_cooldown}" -eq 1 ] \
     && [ "${recover_used}" -lt "${RECOVER_THRESHOLD}" ]; then
    should_migrate_recover=1
  fi
  [ "${should_migrate_recover}" = "1" ] || { echo "FAIL migration recover" >&2; fail=1; }
  if [ "$fail" -ne 0 ]; then
    echo "tokenkey-disk-metrics-edge selftest FAILED" >&2
    exit 1
  fi
  echo "tokenkey-disk-metrics-edge selftest: ok" >&2
  exit 0
fi

COOLDOWN="${TOKENKEY_DISK_ALERT_COOLDOWN_SEC:-1800}"
DISK_ACTIVE_STAMP="/run/tokenkey-disk-alert-active"
WEBHOOK=""; SECRET=""; NODE="$(hostname)"
if [ -r /var/lib/tokenkey/.env ]; then
  WEBHOOK="$(sed -n 's/^TOKENKEY_FEISHU_WEBHOOK_URL=//p' /var/lib/tokenkey/.env | head -1)"
  SECRET="$(sed -n 's/^TOKENKEY_FEISHU_WEBHOOK_SECRET=//p' /var/lib/tokenkey/.env | head -1)"
  DOM="$(sed -n 's/^API_DOMAIN=//p' /var/lib/tokenkey/.env | head -1)"
  [ -n "${DOM}" ] && NODE="${DOM}"
fi

# Self-contained Feishu alert with per-stamp cooldown. $1=stamp file, $2=text.
# Stamp only on body code:0 so a misconfigured webhook keeps retrying.
tk_feishu_alert() {
  local stamp="$1" text="$2" now last sign payload resp
  [ -n "${WEBHOOK}" ] || return 1
  now="$(date +%s)"; last=0
  [ -r "${stamp}" ] && last="$(cat "${stamp}" 2>/dev/null || echo 0)"
  [ "$((now - last))" -ge "${COOLDOWN}" ] || return 0
  if [ -n "${SECRET}" ]; then
    sign="$(printf '' | openssl dgst -sha256 -hmac "${now}"$'\n'"${SECRET}" -binary 2>/dev/null | base64)"
    payload="$(printf '{"timestamp":"%s","sign":"%s","msg_type":"text","content":{"text":"%s"}}' "${now}" "${sign}" "${text}")"
  else
    payload="$(printf '{"msg_type":"text","content":{"text":"%s"}}' "${text}")"
  fi
  if ! resp="$(curl -sS -m 10 -X POST "${WEBHOOK}" -H 'Content-Type: application/json' -d "${payload}" 2>/dev/null)"; then
    return 1
  fi
  case "${resp}" in
    *'"code":0'*) echo "${now}" > "${stamp}" || true; return 0 ;;  # preflight-allow: swallow — best-effort cooldown stamp
    *) return 1 ;;
  esac
}

# Recovery posts bypass cooldown — operators need the paired ✅ once pressure clears.
tk_feishu_post_now() {
  local text="$1" now sign payload resp
  [ -n "${WEBHOOK}" ] || return 1
  now="$(date +%s)"
  if [ -n "${SECRET}" ]; then
    sign="$(printf '' | openssl dgst -sha256 -hmac "${now}"$'\n'"${SECRET}" -binary 2>/dev/null | base64)"
    payload="$(printf '{"timestamp":"%s","sign":"%s","msg_type":"text","content":{"text":"%s"}}' "${now}" "${sign}" "${text}")"
  else
    payload="$(printf '{"msg_type":"text","content":{"text":"%s"}}' "${text}")"
  fi
  if ! resp="$(curl -sS -m 10 -X POST "${WEBHOOK}" -H 'Content-Type: application/json' -d "${payload}" 2>/dev/null)"; then
    return 1
  fi
  case "${resp}" in
    *'"code":0'*) return 0 ;;
    *) return 1 ;;
  esac
}

handle_disk_state() {
  local used="$1" threshold="$2" recover_threshold="$3"
  local active_stamp="$4" cooldown_stamp="$5"
  if [ "${used}" -ge "${threshold}" ]; then
    if tk_feishu_alert "${cooldown_stamp}" \
      "🔴 P0 磁盘将满 ${NODE} — 根盘 / 使用率 ${used}% (阈值 ${threshold}%)。Postgres 满盘会崩溃→网关全挂。立即清 docker 镜像/日志或扩容。node=${NODE}"; then
      echo 1 >"${active_stamp}" 2>/dev/null || true  # preflight-allow: swallow — best-effort latch; timer retries next tick
    fi
  elif [ "${used}" -lt "${recover_threshold}" ]; then
    if [ -r "${active_stamp}" ] || [ -r "${cooldown_stamp}" ]; then
      if tk_feishu_post_now \
        "✅ P0 磁盘压力已恢复 ${NODE} — 根盘 / 使用率 ${used}% (恢复阈值 ${recover_threshold}%，告警阈值 ${threshold}%)。node=${NODE}"; then
        rm -f "${active_stamp}" "${cooldown_stamp}" 2>/dev/null || true  # preflight-allow: swallow — clear latch + legacy cooldown for next incident
      fi
    fi
  fi
}

# --- root disk-full alert + paired recovery ----------------------------------
USED="$(df -P / 2>/dev/null | awk 'NR==2 {gsub(/%/,"",$5); print $5}')"
THRESHOLD="${TOKENKEY_DISK_ALERT_THRESHOLD:-85}"
RECOVER_THRESHOLD="${TOKENKEY_DISK_RECOVER_THRESHOLD:-$((THRESHOLD - 5))}"
if [ "${RECOVER_THRESHOLD}" -ge "${THRESHOLD}" ]; then
  RECOVER_THRESHOLD=$((THRESHOLD - 5))
fi
if [ "${RECOVER_THRESHOLD}" -lt 1 ]; then
  RECOVER_THRESHOLD=1
fi

if [ -n "${USED}" ]; then
  handle_disk_state "${USED}" "${THRESHOLD}" "${RECOVER_THRESHOLD}" \
    "${DISK_ACTIVE_STAMP}" /run/tokenkey-disk-alert.stamp
fi

# --- memory-pressure alert (prod parity; 2026-08-03 us6 OOM) ------------------
# MemAvailable collapse fires while the box is still reachable so operators can
# act before systemd-network / SSM die. micro_3_0 is 1GiB — this is the early page.
MEM_THRESHOLD="${TOKENKEY_MEM_ALERT_THRESHOLD:-90}"
MEMUSEDPCT="$(awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} END{ if(t>0) printf "%d",(t-a)*100/t; else print 0 }' /proc/meminfo 2>/dev/null || echo 0)"
if [ "${MEMUSEDPCT:-0}" -ge "${MEM_THRESHOLD}" ]; then
  SWAPPCT="$(awk '/^SwapTotal:/{t=$2} /^SwapFree:/{f=$2} END{ if(t>0) printf "%d",(t-f)*100/t; else print 0 }' /proc/meminfo 2>/dev/null || echo 0)"
  LOAD1="$(awk '{print $1}' /proc/loadavg 2>/dev/null || echo 0)"
  if ! tk_feishu_alert /run/tokenkey-mem-alert.stamp \
    "🟠 P1 内存压力 ${NODE} — 内存 ${MEMUSEDPCT}% (阈值 ${MEM_THRESHOLD}%), swap ${SWAPPCT}%, load1 ${LOAD1}。1GiB edge 无 headroom 会 OOM kill sub2api/networkd→主机黑洞。立即查重负载/限流或升配。node=${NODE}"; then
    : # Delivery is retried on the next timer tick.
  fi
fi
