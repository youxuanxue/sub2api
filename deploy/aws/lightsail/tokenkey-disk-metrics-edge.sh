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

TK_SELFTEST=0
TK_SELFTEST_DIR=""
if [ "${1:-}" = "--selftest" ]; then
  TK_SELFTEST=1
  TK_SELFTEST_DIR="$(mktemp -d)"
  trap 'rm -rf "${TK_SELFTEST_DIR}"' EXIT
fi

if [ "${TK_SELFTEST}" = "1" ]; then
  # Pure local checks — no network, no .env required. Arithmetic/awk fixtures run
  # here; the alert STATE MACHINE is exercised against the real handle_mem_state
  # at the bottom of this file (a mirrored copy of the branching could drift from
  # the code it claims to protect, which is the whole point of the gate).
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
  # MemTotal is reported, not hardcoded: small_3_0 is ~1.87GiB, not 1GiB.
  TOTAL_MIB="$(printf '%s\n' 'MemTotal: 1959220 kB' | awk '/^MemTotal:/{ printf "%d", $2/1024 }')"
  [ "${TOTAL_MIB}" = "1913" ] || { echo "FAIL mem total mib got=${TOTAL_MIB} want=1913" >&2; fail=1; }
  # Threshold clamps: a misconfigured recover must never invert the hysteresis
  # band, and a critical below the warn line must never page under it.
  MEM_THRESHOLD=90; MEM_RECOVER_THRESHOLD=95
  if [ "${MEM_RECOVER_THRESHOLD}" -ge "${MEM_THRESHOLD}" ]; then MEM_RECOVER_THRESHOLD=$((MEM_THRESHOLD - 10)); fi
  [ "${MEM_RECOVER_THRESHOLD}" = "80" ] || { echo "FAIL mem recover clamp got=${MEM_RECOVER_THRESHOLD} want=80" >&2; fail=1; }
  MEM_CRITICAL_THRESHOLD=80
  if [ "${MEM_CRITICAL_THRESHOLD}" -lt "${MEM_THRESHOLD}" ]; then MEM_CRITICAL_THRESHOLD="${MEM_THRESHOLD}"; fi
  [ "${MEM_CRITICAL_THRESHOLD}" = "90" ] || { echo "FAIL mem critical clamp got=${MEM_CRITICAL_THRESHOLD} want=90" >&2; fail=1; }
  # Execution continues: the state-machine cases at the bottom of this file drive
  # the REAL handle_mem_state, then report and exit.
fi

COOLDOWN="${TOKENKEY_DISK_ALERT_COOLDOWN_SEC:-1800}"
DISK_ACTIVE_STAMP="/run/tokenkey-disk-alert-active"
WEBHOOK=""; SECRET=""; NODE="$(hostname)"
if [ "${TK_SELFTEST}" = "1" ]; then
  # Selftest reads fixture meminfo and records sends instead of posting. Set
  # before the .env read so a developer box with a real webhook can never page.
  TK_MEMINFO_PATH="${TK_SELFTEST_DIR}/meminfo"
  TK_LOADAVG_PATH="${TK_SELFTEST_DIR}/loadavg"
  NODE="selftest-node"
else
  TK_MEMINFO_PATH=/proc/meminfo
  TK_LOADAVG_PATH=/proc/loadavg
fi
if [ "${TK_SELFTEST}" != "1" ] && [ -r /var/lib/tokenkey/.env ]; then
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

if [ "${TK_SELFTEST}" != "1" ] && [ -n "${USED}" ]; then
  handle_disk_state "${USED}" "${THRESHOLD}" "${RECOVER_THRESHOLD}" \
    "${DISK_ACTIVE_STAMP}" /run/tokenkey-disk-alert.stamp
fi

# --- memory-pressure alert + paired recovery (prod parity; 2026-08-03 us6 OOM) -
# MemAvailable collapse fires while the box is still reachable so operators can
# act before systemd-network / SSM die.
#
# 2026-09-27 us5 false page — three defects fixed together:
#   (1) NO recovery pairing. Unlike the disk alert above, a fired mem alert had no
#       latch and no ✅ post, so a transient peak left an operator holding an open
#       P1 ("立即查重负载/限流或升配") against a box that was back to 40% five
#       minutes later. Recovery now mirrors handle_disk_state.
#   (2) Hardcoded "1GiB edge" in the body. Live small_3_0 edges (uk1/uk2/us3/us4/us5/us6)
#       are ~1.87GiB; planned/retired micro_3_0 rows (us1/us2/us7/fra1) stay 1GiB.
#       Misreporting the box class directly misleads the 升配 call. The body now
#       carries the real MemTotal.
#   (3) Single-sample firing on a volatile estimator. MemAvailable on a 2GiB box
#       running a Go app is genuinely spiky (measured 212MiB→341MiB RSS in 40s of
#       GC sawtooth), and us5 paged at an inter-sample peak while sar's 10-min
#       samples either side read 43%/46%, load1 0.47, swap 8%, zero OOM kills. A
#       threshold crossing must now be confirmed by a CONSECUTIVE tick — the same
#       min-sample principle the gateway evaluator already applies to ratio metrics.
#       MEM_CRITICAL_THRESHOLD still pages on the first sample, so a genuine OOM
#       march does not pay the 5-min confirmation delay.
MEM_THRESHOLD="${TOKENKEY_MEM_ALERT_THRESHOLD:-90}"
MEM_CRITICAL_THRESHOLD="${TOKENKEY_MEM_CRITICAL_THRESHOLD:-96}"
MEM_RECOVER_THRESHOLD="${TOKENKEY_MEM_RECOVER_THRESHOLD:-$((MEM_THRESHOLD - 10))}"
if [ "${MEM_RECOVER_THRESHOLD}" -ge "${MEM_THRESHOLD}" ]; then
  MEM_RECOVER_THRESHOLD=$((MEM_THRESHOLD - 10))
fi
if [ "${MEM_RECOVER_THRESHOLD}" -lt 1 ]; then
  MEM_RECOVER_THRESHOLD=1
fi
if [ "${MEM_CRITICAL_THRESHOLD}" -lt "${MEM_THRESHOLD}" ]; then
  MEM_CRITICAL_THRESHOLD="${MEM_THRESHOLD}"
fi
MEM_ACTIVE_STAMP="/run/tokenkey-mem-alert-active"
MEM_PENDING_STAMP="/run/tokenkey-mem-alert-pending"
MEM_COOLDOWN_STAMP="/run/tokenkey-mem-alert.stamp"

# $1=used pct  $2=warn threshold  $3=critical threshold  $4=recover threshold
handle_mem_state() {
  local used="$1" threshold="$2" critical="$3" recover_threshold="$4"
  local total_mib swappct load1 fire=0

  if [ "${used}" -ge "${threshold}" ]; then
    # Critical pages on sight; the ordinary threshold needs a confirming tick so
    # one GC-sawtooth sample cannot page.
    if [ "${used}" -ge "${critical}" ]; then
      fire=1
    elif [ -r "${MEM_PENDING_STAMP}" ]; then
      fire=1
    else
      echo 1 >"${MEM_PENDING_STAMP}" 2>/dev/null || true  # preflight-allow: swallow — best-effort candidate mark; timer re-reads next tick
      return 0
    fi
  fi

  if [ "${fire}" = "1" ]; then
    total_mib="$(awk '/^MemTotal:/{ printf "%d", $2/1024 }' "${TK_MEMINFO_PATH}" 2>/dev/null || echo 0)"
    swappct="$(awk '/^SwapTotal:/{t=$2} /^SwapFree:/{f=$2} END{ if(t>0) printf "%d",(t-f)*100/t; else print 0 }' "${TK_MEMINFO_PATH}" 2>/dev/null || echo 0)"
    load1="$(awk '{print $1}' "${TK_LOADAVG_PATH}" 2>/dev/null || echo 0)"
    if tk_feishu_alert "${MEM_COOLDOWN_STAMP}" \
      "🟠 P1 内存压力 ${NODE} — 内存 ${used}% (阈值 ${threshold}%, 本机共 ${total_mib}MiB), swap ${swappct}%, load1 ${load1}。无 headroom 会 OOM kill sub2api/networkd→主机黑洞。立即查重负载/限流或升配。node=${NODE}"; then
      echo 1 >"${MEM_ACTIVE_STAMP}" 2>/dev/null || true  # preflight-allow: swallow — best-effort latch; timer retries next tick
    fi
    rm -f "${MEM_PENDING_STAMP}" 2>/dev/null || true  # preflight-allow: swallow — crossing consumed; next incident re-arms
    return 0
  fi

  # Below the alert threshold: this tick is no longer a crossing candidate.
  rm -f "${MEM_PENDING_STAMP}" 2>/dev/null || true  # preflight-allow: swallow — best-effort; a stale pending only costs one extra confirm
  if [ "${used}" -lt "${recover_threshold}" ]; then
    if [ -r "${MEM_ACTIVE_STAMP}" ] || [ -r "${MEM_COOLDOWN_STAMP}" ]; then
      total_mib="$(awk '/^MemTotal:/{ printf "%d", $2/1024 }' "${TK_MEMINFO_PATH}" 2>/dev/null || echo 0)"
      swappct="$(awk '/^SwapTotal:/{t=$2} /^SwapFree:/{f=$2} END{ if(t>0) printf "%d",(t-f)*100/t; else print 0 }' "${TK_MEMINFO_PATH}" 2>/dev/null || echo 0)"
      if tk_feishu_post_now \
        "✅ P1 内存压力已恢复 ${NODE} — 内存 ${used}% (恢复阈值 ${recover_threshold}%，告警阈值 ${threshold}%, 本机共 ${total_mib}MiB), swap ${swappct}%。node=${NODE}"; then
        rm -f "${MEM_ACTIVE_STAMP}" "${MEM_COOLDOWN_STAMP}" 2>/dev/null || true  # preflight-allow: swallow — clear latch + cooldown for next incident
      fi
    fi
  fi
}

if [ "${TK_SELFTEST}" = "1" ]; then
  # --- state-machine cases driving the REAL handle_mem_state above -------------
  # Stamps and meminfo live in the temp fixture dir; the senders are replaced by
  # recorders. Nothing here touches /proc, /run or the network.
  MEM_ACTIVE_STAMP="${TK_SELFTEST_DIR}/mem-active"
  MEM_PENDING_STAMP="${TK_SELFTEST_DIR}/mem-pending"
  MEM_COOLDOWN_STAMP="${TK_SELFTEST_DIR}/mem-cooldown"
  SENT_LOG="${TK_SELFTEST_DIR}/sent"
  echo "0.47 0.20 0.18 1/400 1234" > "${TK_LOADAVG_PATH}"

  tk_feishu_alert() { echo "ALERT ${2}" >> "${SENT_LOG}"; echo 1 > "$1"; return 0; }
  tk_feishu_post_now() { echo "RECOVERY ${1}" >> "${SENT_LOG}"; return 0; }

  # Drive one timer tick at a given %used, via the same MemAvailable arithmetic
  # the live path uses (so the estimator itself stays under test).
  tk_tick() {
    local used="$1" total=1959220
    {
      echo "MemTotal:        ${total} kB"
      echo "MemAvailable:    $(( total * (100 - used) / 100 )) kB"
      echo "SwapTotal:       2097148 kB"
      echo "SwapFree:        1958908 kB"
    } > "${TK_MEMINFO_PATH}"
    local pct
    pct="$(awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} END{ if(t>0) printf "%d",(t-a)*100/t; else print 0 }' "${TK_MEMINFO_PATH}")"
    handle_mem_state "${pct}" 90 96 80
  }
  tk_reset() { rm -f "${SENT_LOG}" "${MEM_ACTIVE_STAMP}" "${MEM_PENDING_STAMP}" "${MEM_COOLDOWN_STAMP}"; : > "${SENT_LOG}"; }
  # grep -c prints 0 AND exits non-zero on no-match, so `|| echo 0` would emit
  # "0\n0" and break every zero-assertion. Count with awk instead.
  tk_count() { awk -v p="^$1" 'index($0,substr(p,2))==1 {n++} END{ printf "%d", n+0 }' "${SENT_LOG}" 2>/dev/null; }

  # Case 1 — the us5 false page: one 91% spike then recovery sends NOTHING.
  tk_reset; tk_tick 91
  [ "$(tk_count ALERT)" = "0" ] || { echo "FAIL single 91% spike paged (us5 regression)" >&2; fail=1; }
  [ -r "${MEM_PENDING_STAMP}" ] || { echo "FAIL 91% spike did not arm a pending crossing" >&2; fail=1; }
  tk_tick 40
  [ "$(tk_count ALERT)" = "0" ] || { echo "FAIL transient spike paged after it cleared" >&2; fail=1; }
  [ "$(tk_count RECOVERY)" = "0" ] || { echo "FAIL recovery posted without a prior alert" >&2; fail=1; }

  # Case 2 — sustained pressure pages once, then pairs exactly one ✅.
  tk_reset; tk_tick 91; tk_tick 91
  [ "$(tk_count ALERT)" = "1" ] || { echo "FAIL sustained 91% did not page exactly once got=$(tk_count ALERT)" >&2; fail=1; }
  grep -q '本机共 1913MiB' "${SENT_LOG}" 2>/dev/null || { echo "FAIL alert body missing real MemTotal" >&2; fail=1; }
  grep -q '1GiB edge' "${SENT_LOG}" 2>/dev/null && { echo "FAIL alert body hardcodes 1GiB" >&2; fail=1; }
  tk_tick 40
  [ "$(tk_count RECOVERY)" = "1" ] || { echo "FAIL recovery not paired got=$(tk_count RECOVERY)" >&2; fail=1; }
  [ -r "${MEM_ACTIVE_STAMP}" ] && { echo "FAIL latch not cleared after recovery" >&2; fail=1; }

  # Case 3 — a genuine OOM march pages on the FIRST sample, no confirm delay.
  tk_reset; tk_tick 97
  [ "$(tk_count ALERT)" = "1" ] || { echo "FAIL critical 97% did not page immediately" >&2; fail=1; }

  # Case 4 — two unrelated spikes split by a quiet tick must not add up.
  tk_reset; tk_tick 91; tk_tick 85; tk_tick 91
  [ "$(tk_count ALERT)" = "0" ] || { echo "FAIL non-consecutive spikes paged" >&2; fail=1; }

  # Case 5 — hysteresis band: no premature all-clear between 80% and 90%.
  tk_reset; tk_tick 97; tk_tick 85
  [ "$(tk_count RECOVERY)" = "0" ] || { echo "FAIL all-clear inside hysteresis band" >&2; fail=1; }

  if [ "$fail" -ne 0 ]; then
    echo "tokenkey-disk-metrics-edge selftest FAILED" >&2
    exit 1
  fi
  echo "tokenkey-disk-metrics-edge selftest: ok" >&2
  exit 0
fi

MEMUSEDPCT="$(awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} END{ if(t>0) printf "%d",(t-a)*100/t; else print 0 }' "${TK_MEMINFO_PATH}" 2>/dev/null || echo 0)"
handle_mem_state "${MEMUSEDPCT:-0}" "${MEM_THRESHOLD}" "${MEM_CRITICAL_THRESHOLD}" "${MEM_RECOVER_THRESHOLD}"
