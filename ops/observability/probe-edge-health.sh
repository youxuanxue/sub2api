#!/bin/bash
# probe-edge-health.sh — TokenKey read-only edge-health snapshot.
#
# Runs INSIDE a Stage0 host (edge or prod) via SSM AWS-RunShellScript + base64
# delivery (use ops/observability/run-probe.sh). Pure read-only: psql SELECT +
# `docker logs` grep. Emits the schedulable-account roster and the access-log
# served_200 vs no_available_429 counts as tagged, field-named JSON lines for the
# Python verdict sibling edge_health_verdict.py:
#
#   ACCT    {"id":1,"name":"..","platform":"anthropic","status":"active",
#            "schedulable":true,"concurrency":16,"session_window_status":"allowed"}
#   TRAFFIC {"since":"2h","served_200":..,"all_429":..,"no_available_429":..,
#            "wait_timeout":..,"client_cancel":..,"total_completed":..}
#
# WHY (2026-06-06 yace load test): prod's "upstream-429 by account" could not tell a
# dead edge (us3: 0x200, 33748x"no available"429) from a healthy one (us5: 2251x200,
# 77x429) — both read ~1300 upstream-429 on prod. The edge's OWN served_200 :
# no_available_429 ratio + schedulable-account count is the only reliable signal.
# This probe collects exactly that; the verdict (healthy/thin/degraded/down) is the
# Python sibling so the threshold logic stays unit-testable (--selftest in preflight).
#
# Determinism contract (dev-rules-convention.mdc §"skill / command 确定性基线"):
#   field names embedded next to values (row_to_json); no positional parsing.
#
# Env:
#   PLATFORM    account platform to roster + judge. Default anthropic.
#   SINCE       docker logs --since window for the traffic counts. Default 2h.
#               (For a past burst post-mortem pass e.g. SINCE=15h, then read the
#                counts as "since N hours ago" — this probe does not slice to an
#                exact UTC window; for that use parse-access-log.py on a pull.)
#   CONTAINER   gateway container name. Default auto. auto resolves
#               /var/lib/tokenkey/active-color to tokenkey-blue/green and
#               falls back to the legacy tokenkey container.
#   ACTIVE_COLOR_FILE
#               active-color file path for CONTAINER=auto
#               (default /var/lib/tokenkey/active-color; test seam).
#
# Not pipefail (grep -c exits 1 on zero matches and we WANT the 0); set -u only.
set -u

PLATFORM="${PLATFORM:-anthropic}"
SINCE="${SINCE:-2h}"
CONTAINER_INPUT="${CONTAINER:-auto}"
ACTIVE_COLOR_FILE="${ACTIVE_COLOR_FILE:-/var/lib/tokenkey/active-color}"
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

resolve_gateway_container() {
  local requested="$1"
  if [ "$requested" != "auto" ]; then
    printf '%s\n' "$requested"
    return 0
  fi

  local color candidate
  if [ -f "$ACTIVE_COLOR_FILE" ]; then
    color="$(tr -d '[:space:]' < "$ACTIVE_COLOR_FILE" 2>/dev/null || true)"  # preflight-allow: swallow - unreadable active-color falls back to legacy container
    case "$color" in
      blue|green)
        candidate="tokenkey-$color"
        if docker inspect "$candidate" >/dev/null 2>&1; then
          printf '%s\n' "$candidate"
          return 0
        fi
        ;;
    esac
  fi

  for candidate in tokenkey tokenkey-blue tokenkey-green; do
    if docker inspect "$candidate" >/dev/null 2>&1; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done

  printf '%s\n' tokenkey
}

CONTAINER="$(resolve_gateway_container "$CONTAINER_INPUT")"

echo "=== docker ps (tokenkey stack) ==="
docker ps --filter name=tokenkey --format '{{.Names}}\t{{.Status}}' 2>/dev/null || true  # preflight-allow: swallow — diagnostic header only

echo "=== ACCT roster (platform=$PLATFORM; field names embedded) ==="
# One JSON object per account, prefixed ACCT. session_window_status is a top-level
# column; schedulable/concurrency/status are top-level columns (NOT extra) — see the
# field-source table in the tokenkey-online-traffic-profile skill.
$PSQL -tAc "
SELECT 'ACCT '||row_to_json(t)::text FROM (
  SELECT id, name, platform, status, schedulable, concurrency,
         session_window_status,
         (extra->>'stability_tier') AS tier
  FROM accounts
  WHERE platform = '${PLATFORM}' AND deleted_at IS NULL
  ORDER BY id
) t;
" 2>&1

echo "=== TRAFFIC (access-log counts over --since $SINCE) ==="
echo "CONTAINER input=$CONTAINER_INPUT resolved=$CONTAINER"
LOGF="$(mktemp /tmp/eh_full.XXXXXX)"
docker logs "$CONTAINER" --since "$SINCE" >"$LOGF" 2>&1 || true  # preflight-allow: swallow — no-logs window is a valid 0-count, not a probe failure
COMPLETED="$(grep -F 'http request completed' "$LOGF" 2>/dev/null || true)"  # preflight-allow: swallow — grep exit 1 on zero matches is the wanted empty result

# served_200 / all_429: tolerate "status_code":200 and "status_code": 200 (build-dependent spacing).
# Every `|| true` below absorbs grep -c's exit 1 on zero matches — a 0 count IS the answer, not an error.
SERVED_200="$(printf '%s' "$COMPLETED" | grep -cE '"status_code":[[:space:]]*200' || true)"  # preflight-allow: swallow — zero-match => 0
ALL_429="$(printf '%s' "$COMPLETED" | grep -cE '"status_code":[[:space:]]*429' || true)"  # preflight-allow: swallow — zero-match => 0
TOTAL_COMPLETED="$(printf '%s' "$COMPLETED" | grep -c . || true)"  # preflight-allow: swallow — zero-match => 0
# Structured markers (robust, not status-line dependent):
NO_AVAIL="$(grep -cF 'select_account_no_available' "$LOGF" 2>/dev/null || true)"  # preflight-allow: swallow — zero-match => 0
WAIT_TIMEOUT="$(grep -cF 'wait timeout' "$LOGF" 2>/dev/null || true)"  # preflight-allow: swallow — zero-match => 0
CLIENT_CANCEL="$(grep -cF 'context canceled' "$LOGF" 2>/dev/null || true)"  # preflight-allow: swallow — zero-match => 0
rm -f "$LOGF"

printf 'TRAFFIC {"since":"%s","served_200":%s,"all_429":%s,"no_available_429":%s,"wait_timeout":%s,"client_cancel":%s,"total_completed":%s}\n' \
  "$SINCE" "${SERVED_200:-0}" "${ALL_429:-0}" "${NO_AVAIL:-0}" "${WAIT_TIMEOUT:-0}" "${CLIENT_CANCEL:-0}" "${TOTAL_COMPLETED:-0}"
