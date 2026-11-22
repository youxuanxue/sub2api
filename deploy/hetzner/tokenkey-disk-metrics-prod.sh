#!/bin/bash
# TokenKey Stage0 host — on-box disk-full + memory-pressure Feishu alerts
# Production host disk and memory alert helper.
#
# Single source of record: consumed by
#   - deploy/hetzner/render-prod-bootstrap.sh (installs it on the prod host).
#   - data-machine tokenkey-disk-metrics.timer (same script; PG cgroup anon path).
# Edit ONLY this file for shared host-pressure alert logic.
# Data-plane PG alert design: docs/approved/design-data-plane-split.md §数据机观测
#
# Differences vs the prod EC2 alert (intentional):
#   - Root is always checked; TOKENKEY_DATA_VOLUME_PATH additionally requires
#     a separate mounted data volume.
#   - NO CloudWatch put-metric.
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
ALERT_ENV_FILE="${TOKENKEY_HOST_ALERT_ENV_FILE:-/var/lib/tokenkey/.env}"
if [ "${TK_SELFTEST}" != "1" ] && [ -r "${ALERT_ENV_FILE}" ]; then
  WEBHOOK="$(sed -n 's/^TOKENKEY_FEISHU_WEBHOOK_URL=//p' "${ALERT_ENV_FILE}" | head -1)"
  SECRET="$(sed -n 's/^TOKENKEY_FEISHU_WEBHOOK_SECRET=//p' "${ALERT_ENV_FILE}" | head -1)"
  DOM="$(sed -n 's/^API_DOMAIN=//p' "${ALERT_ENV_FILE}" | head -1)"
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
  local active_stamp="$4" cooldown_stamp="$5" label="${6:-根盘 /}"
  if [ "${used}" -ge "${threshold}" ]; then
    if tk_feishu_alert "${cooldown_stamp}" \
      "🔴 P0 磁盘将满 ${NODE} — ${label} 使用率 ${used}% (阈值 ${threshold}%)。Postgres 满盘会崩溃→网关全挂。立即清 docker 镜像/日志或扩容。node=${NODE}"; then
      echo 1 >"${active_stamp}" 2>/dev/null || true  # preflight-allow: swallow — best-effort latch; timer retries next tick
    fi
  elif [ "${used}" -lt "${recover_threshold}" ]; then
    if [ -r "${active_stamp}" ] || [ -r "${cooldown_stamp}" ]; then
      if tk_feishu_post_now \
        "✅ P0 磁盘压力已恢复 ${NODE} — ${label} 使用率 ${used}% (恢复阈值 ${recover_threshold}%，告警阈值 ${threshold}%)。node=${NODE}"; then
        rm -f "${active_stamp}" "${cooldown_stamp}" 2>/dev/null || true  # preflight-allow: swallow — clear latch + legacy cooldown for next incident
      fi
    fi
  fi
}

# An absent required mount must never be measured as its underlying root disk.
# Keep mount-loss and pressure latches separate: remount is not disk recovery.
handle_disk_target() {
  local target="$1" label="$2" active="$3" cooldown="$4" require_mount="${5:-0}"
  local used missing="${active}-mount-missing"
  if [ "${require_mount}" = "1" ] && ! mountpoint -q "${target}"; then
    echo "ERROR required data volume is not mounted: ${target}" >&2
    tk_feishu_alert "${missing}" "🔴 P0 数据卷未挂载 ${NODE} — ${target}。磁盘使用率未知，不能当作根盘健康。node=${NODE}" || true # preflight-allow: swallow — service still fails below; absent volume can also hide webhook env
    return 1
  fi
  if ! used="$(df -P "${target}" | awk 'NR==2 {gsub(/%/,"",$5); print $5}')" || [[ ! "${used}" =~ ^[0-9]+$ ]]; then
    echo "ERROR cannot read disk usage: ${target}" >&2
    return 1
  fi
  if [ -r "${missing}" ]; then
    if tk_feishu_post_now "✅ P0 数据卷挂载已恢复 ${NODE} — ${target}，使用率 ${used}%。node=${NODE}"; then
      rm -f "${missing}"
    fi
  fi
  handle_disk_state "${used}" "${THRESHOLD}" "${RECOVER_THRESHOLD}" "${active}" "${cooldown}" "${label}"
}

THRESHOLD="${TOKENKEY_DISK_ALERT_THRESHOLD:-85}"
RECOVER_THRESHOLD="${TOKENKEY_DISK_RECOVER_THRESHOLD:-$((THRESHOLD - 5))}"
if [ "${RECOVER_THRESHOLD}" -ge "${THRESHOLD}" ]; then
  RECOVER_THRESHOLD=$((THRESHOLD - 5))
fi
if [ "${RECOVER_THRESHOLD}" -lt 1 ]; then
  RECOVER_THRESHOLD=1
fi

DISK_STATUS=0
if [ "${TK_SELFTEST}" != "1" ]; then
  handle_disk_target / "根盘 /" "${DISK_ACTIVE_STAMP}" /run/tokenkey-disk-alert.stamp || DISK_STATUS=1
  if [ -n "${TOKENKEY_DATA_VOLUME_PATH:-}" ]; then
    handle_disk_target "${TOKENKEY_DATA_VOLUME_PATH}" "数据卷 ${TOKENKEY_DATA_VOLUME_PATH}" \
      /run/tokenkey-data-disk-alert-active /run/tokenkey-data-disk-alert.stamp 1 || DISK_STATUS=1
  fi
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
#   (2) Hardcoded host size in the body; the alert carries the actual MemTotal.
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

# PG cgroup unreclaimable (anon) pressure — see live path + design-data-plane-split.
PG_CGROUP_ALERT="${TOKENKEY_PG_CGROUP_ALERT:-auto}"
PG_CONTAINER="${TOKENKEY_PG_CONTAINER:-tokenkey-postgres}"
PG_ANON_WARN_MIB="${TOKENKEY_PG_CGROUP_ANON_WARN_MIB:-1536}"
PG_ANON_CRIT_MIB="${TOKENKEY_PG_CGROUP_ANON_CRIT_MIB:-3072}"
PG_CGROUP_ACTIVE_STAMP="/run/tokenkey-pg-cgroup-alert-active"
PG_CGROUP_PENDING_STAMP="/run/tokenkey-pg-cgroup-alert-pending"
PG_CGROUP_COOLDOWN_STAMP="/run/tokenkey-pg-cgroup-alert.stamp"

# $1=anon_mib $2=warn_mib $3=crit_mib $4=oom_events $5=current_mib $6=limit_mib
handle_pg_cgroup_state() {
  local anon_mib="$1" warn_mib="$2" crit_mib="$3" oom="$4" current_mib="$5" limit_mib="$6"
  local fire=0 fill_pct=0

  if [ "${oom:-0}" -gt 0 ] 2>/dev/null; then
    fire=1
  elif [ "${anon_mib}" -ge "${crit_mib}" ] 2>/dev/null; then
    fire=1
  elif [ "${anon_mib}" -ge "${warn_mib}" ] 2>/dev/null; then
    if [ -r "${PG_CGROUP_PENDING_STAMP}" ]; then
      fire=1
    else
      echo 1 >"${PG_CGROUP_PENDING_STAMP}" 2>/dev/null || true  # preflight-allow: swallow — best-effort pending
      return 0
    fi
  fi

  if [ "${fire}" = "1" ]; then
    if [ "${limit_mib}" -gt 0 ] 2>/dev/null; then
      fill_pct=$(( current_mib * 100 / limit_mib ))
    fi
    if tk_feishu_alert "${PG_CGROUP_COOLDOWN_STAMP}" \
      "🟠 P1 PG cgroup 不可回收内存 ${NODE} — anon ${anon_mib}MiB (warn ${warn_mib}/crit ${crit_mib}), cgroup ${current_mib}/${limit_mib}MiB (${fill_pct}%), oom_events=${oom}。file cache 顶满不告；anon 抬升才是尖刺/泄漏信号。查活跃查询/临时文件或升配。container=${PG_CONTAINER} node=${NODE}"; then
      echo 1 >"${PG_CGROUP_ACTIVE_STAMP}" 2>/dev/null || true  # preflight-allow: swallow — best-effort latch
    fi
    rm -f "${PG_CGROUP_PENDING_STAMP}" 2>/dev/null || true  # preflight-allow: swallow
    return 0
  fi

  rm -f "${PG_CGROUP_PENDING_STAMP}" 2>/dev/null || true  # preflight-allow: swallow
  if [ "${anon_mib}" -lt $(( warn_mib * 70 / 100 )) ] 2>/dev/null; then
    if [ -r "${PG_CGROUP_ACTIVE_STAMP}" ] || [ -r "${PG_CGROUP_COOLDOWN_STAMP}" ]; then
      if tk_feishu_post_now \
        "✅ P1 PG cgroup anon 已恢复 ${NODE} — anon ${anon_mib}MiB (warn ${warn_mib}), cgroup ${current_mib}/${limit_mib}MiB。container=${PG_CONTAINER} node=${NODE}"; then
        rm -f "${PG_CGROUP_ACTIVE_STAMP}" "${PG_CGROUP_COOLDOWN_STAMP}" 2>/dev/null || true  # preflight-allow: swallow
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

  # Real disk collector + state machine: root remains healthy while the
  # independent prod volume fills, stays in hysteresis, then recovers once.
  df() { printf 'Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/test 100 90 10 %s%% %s\n' "${TEST_DISK_USED}" "$2"; }
  mountpoint() { [ "${TEST_MOUNTED}" = "1" ]; }
  TEST_MOUNTED=1; TEST_DISK_USED=11
  ROOT_ACTIVE="${TK_SELFTEST_DIR}/root-active"; ROOT_COOLDOWN="${TK_SELFTEST_DIR}/root-cooldown"
  DATA_ACTIVE="${TK_SELFTEST_DIR}/data-active"; DATA_COOLDOWN="${TK_SELFTEST_DIR}/data-cooldown"
  tk_reset
  handle_disk_target / "根盘 /" "${ROOT_ACTIVE}" "${ROOT_COOLDOWN}"
  TEST_DISK_USED=90
  handle_disk_target /var/lib/tokenkey "数据卷 /var/lib/tokenkey" "${DATA_ACTIVE}" "${DATA_COOLDOWN}" 1
  [ "$(tk_count ALERT)" = "1" ] && [ -r "${DATA_ACTIVE}" ] && [ ! -r "${ROOT_ACTIVE}" ] || { echo "FAIL data disk did not alert independently" >&2; fail=1; }
  grep -q '数据卷 /var/lib/tokenkey 使用率 90%' "${SENT_LOG}" || { echo "FAIL data alert label" >&2; fail=1; }
  TEST_DISK_USED=82
  handle_disk_target /var/lib/tokenkey data "${DATA_ACTIVE}" "${DATA_COOLDOWN}" 1
  [ "$(tk_count RECOVERY)" = "0" ] || { echo "FAIL disk recovery inside hysteresis" >&2; fail=1; }
  TEST_MOUNTED=0; TEST_DISK_USED=11
  if handle_disk_target /var/lib/tokenkey data "${DATA_ACTIVE}" "${DATA_COOLDOWN}" 1 2>/dev/null; then
    echo "FAIL absent data mount accepted as healthy root" >&2; fail=1
  fi
  [ -r "${DATA_ACTIVE}" ] && [ "$(tk_count RECOVERY)" = "0" ] || { echo "FAIL absent mount cleared pressure" >&2; fail=1; }
  TEST_MOUNTED=1; TEST_DISK_USED=75
  handle_disk_target /var/lib/tokenkey data "${DATA_ACTIVE}" "${DATA_COOLDOWN}" 1
  [ "$(tk_count RECOVERY)" = "2" ] && [ ! -r "${DATA_ACTIVE}" ] || { echo "FAIL paired mount and pressure recovery" >&2; fail=1; }
  handle_disk_target /var/lib/tokenkey data "${DATA_ACTIVE}" "${DATA_COOLDOWN}" 1
  [ "$(tk_count RECOVERY)" = "2" ] || { echo "FAIL duplicate disk recovery" >&2; fail=1; }
  TEST_DISK_USED=unknown
  if handle_disk_target /var/lib/tokenkey data "${DATA_ACTIVE}" "${DATA_COOLDOWN}" 1 2>/dev/null; then
    echo "FAIL invalid df accepted" >&2; fail=1
  fi

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
  grep -q '1GiB host' "${SENT_LOG}" 2>/dev/null && { echo "FAIL alert body hardcodes 1GiB" >&2; fail=1; }
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

  # --- PG cgroup anon state machine (cache-full must not page) -----------------
  PG_CGROUP_ACTIVE_STAMP="${TK_SELFTEST_DIR}/pg-cgroup-active"
  PG_CGROUP_PENDING_STAMP="${TK_SELFTEST_DIR}/pg-cgroup-pending"
  PG_CGROUP_COOLDOWN_STAMP="${TK_SELFTEST_DIR}/pg-cgroup-cooldown"
  tk_pg_reset() {
    rm -f "${SENT_LOG}" "${PG_CGROUP_ACTIVE_STAMP}" "${PG_CGROUP_PENDING_STAMP}" "${PG_CGROUP_COOLDOWN_STAMP}"
    : > "${SENT_LOG}"
  }
  # args: anon_mib [oom=0]
  tk_pg_tick() {
    handle_pg_cgroup_state "$1" 1536 3072 "${2:-0}" 5900 6144
  }

  # Cache-full with low anon: never pages (the prod-data false signal we fixed).
  tk_pg_reset; tk_pg_tick 300; tk_pg_tick 300
  [ "$(tk_count ALERT)" = "0" ] || { echo "FAIL cache-full low-anon paged" >&2; fail=1; }

  # Single warn-level anon spike needs confirm.
  tk_pg_reset; tk_pg_tick 1600
  [ "$(tk_count ALERT)" = "0" ] || { echo "FAIL single anon warn paged" >&2; fail=1; }
  [ -r "${PG_CGROUP_PENDING_STAMP}" ] || { echo "FAIL anon warn did not arm pending" >&2; fail=1; }
  tk_pg_tick 400
  [ "$(tk_count ALERT)" = "0" ] || { echo "FAIL transient anon warn paged after clear" >&2; fail=1; }

  # Sustained warn pages once; 70% of warn(=1536) is 1075 — stay above that in-band.
  tk_pg_reset; tk_pg_tick 1600; tk_pg_tick 1600
  [ "$(tk_count ALERT)" = "1" ] || { echo "FAIL sustained anon warn did not page got=$(tk_count ALERT)" >&2; fail=1; }
  grep -q '不可回收内存' "${SENT_LOG}" || { echo "FAIL pg cgroup alert body" >&2; fail=1; }
  tk_pg_tick 1200
  [ "$(tk_count RECOVERY)" = "0" ] || { echo "FAIL pg recovery inside hysteresis" >&2; fail=1; }
  tk_pg_tick 400
  [ "$(tk_count RECOVERY)" = "1" ] || { echo "FAIL pg recovery not paired got=$(tk_count RECOVERY)" >&2; fail=1; }

  # Critical anon pages immediately.
  tk_pg_reset; tk_pg_tick 3200
  [ "$(tk_count ALERT)" = "1" ] || { echo "FAIL critical anon did not page immediately" >&2; fail=1; }

  # oom_events>0 pages even with low anon.
  tk_pg_reset; tk_pg_tick 100 2
  [ "$(tk_count ALERT)" = "1" ] || { echo "FAIL oom_events did not page" >&2; fail=1; }

  if [ "$fail" -ne 0 ]; then
    echo "tokenkey-disk-metrics-prod selftest FAILED" >&2
    exit 1
  fi
  echo "tokenkey-disk-metrics-prod selftest: ok" >&2
  exit 0
fi

MEMUSEDPCT="$(awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} END{ if(t>0) printf "%d",(t-a)*100/t; else print 0 }' "${TK_MEMINFO_PATH}" 2>/dev/null || echo 0)"
handle_mem_state "${MEMUSEDPCT:-0}" "${MEM_THRESHOLD}" "${MEM_CRITICAL_THRESHOLD}" "${MEM_RECOVER_THRESHOLD}"

# PG cgroup anon alert (data hosts with tokenkey-postgres). Cache-full ≠ alert.
# Design: docs/approved/design-data-plane-split.md §数据机观测
tk_pg_cgroup_sample() {
  local name="$1" cid scope current max anon oom
  cid="$(docker inspect -f '{{.Id}}' "${name}" 2>/dev/null || true)"
  [ -n "${cid}" ] || return 1
  scope="/sys/fs/cgroup/system.slice/docker-${cid}.scope"
  [ -d "${scope}" ] || scope="$(find /sys/fs/cgroup -type d -name "docker-${cid}.scope" 2>/dev/null | head -1)"
  [ -n "${scope}" ] && [ -d "${scope}" ] || return 1
  current="$(cat "${scope}/memory.current" 2>/dev/null || echo 0)"
  max="$(cat "${scope}/memory.max" 2>/dev/null || echo max)"
  case "${max}" in
    max|"") max=0 ;;
  esac
  anon="$(awk '/^anon /{print $2; exit}' "${scope}/memory.stat" 2>/dev/null || echo 0)"
  oom="$(awk '/^oom /{print $2; exit}' "${scope}/memory.events" 2>/dev/null || echo 0)"
  printf '%s %s %s %s\n' "${current:-0}" "${max:-0}" "${anon:-0}" "${oom:-0}"
}

tk_pg_cgroup_should_run() {
  case "${PG_CGROUP_ALERT}" in
    0|false|off|no) return 1 ;;
    1|true|on|yes) return 0 ;;
    auto|*)
      docker inspect -f '{{.State.Running}}' "${PG_CONTAINER}" 2>/dev/null | grep -qx true
      ;;
  esac
}

if tk_pg_cgroup_should_run; then
  if sample="$(tk_pg_cgroup_sample "${PG_CONTAINER}")"; then
    # shellcheck disable=SC2086
    set -- ${sample}
    cur_b="${1:-0}"; max_b="${2:-0}"; anon_b="${3:-0}"; oom_n="${4:-0}"
    anon_mib=$(( anon_b / 1024 / 1024 ))
    cur_mib=$(( cur_b / 1024 / 1024 ))
    lim_mib=$(( max_b / 1024 / 1024 ))
    handle_pg_cgroup_state "${anon_mib}" "${PG_ANON_WARN_MIB}" "${PG_ANON_CRIT_MIB}" "${oom_n}" "${cur_mib}" "${lim_mib}"
  fi
fi

exit "${DISK_STATUS}"
