#!/usr/bin/env bash
# probe-release-control-plane.sh — read-only post-release control-plane health.
#
# Checks prod /health (api.* gateway) + /api/v1/settings/public (apex human
# surface after phase 2).
# This mechanizes the tokenkey-stage0-release-rollout follow-up control-plane
# step so operators do not hand-roll jq/curl loops during a release.
#
# Output:
#   - one JSON object per probe on stdout;
#   - a final SUMMARY JSON object with ok/total/failures.
#
# Env:
#   PROD_BASE_URL=https://api.tokenkey.dev  gateway /health (machine path)
#   PROD_SITE_BASE_URL optional; defaults from PROD_BASE_URL (api.* → apex)
#
# Usage:
#   bash ops/observability/probe-release-control-plane.sh
#
# Exit:
#   0 when every probe returns HTTP 200; 4 when any probe fails.
set -euo pipefail

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
fi
if [ "$#" -gt 0 ]; then
  echo "probe-release-control-plane: unknown arg: $1" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

# shellcheck source=../stage0/smoke_lib.sh
source "${REPO_ROOT}/ops/stage0/smoke_lib.sh"

PROD_BASE_URL="${PROD_BASE_URL:-https://api.tokenkey.dev}"
PROD_SITE_BASE_URL="$(smoke_human_base_url "${PROD_BASE_URL}")"
CURL_TIMEOUT_SECONDS="${CURL_TIMEOUT_SECONDS:-10}"
INCLUDE_SETTINGS_PUBLIC="${INCLUDE_SETTINGS_PUBLIC:-1}"

case "$CURL_TIMEOUT_SECONDS" in
  ''|*[!0-9]*) echo "probe-release-control-plane: CURL_TIMEOUT_SECONDS must be positive integer" >&2; exit 2 ;;
esac
if [ "$CURL_TIMEOUT_SECONDS" -lt 1 ]; then
  echo "probe-release-control-plane: CURL_TIMEOUT_SECONDS must be >= 1" >&2
  exit 2
fi

declare -a PROBES=()
PROBES+=("prod health ${PROD_BASE_URL%/}/health")
if [ "$INCLUDE_SETTINGS_PUBLIC" = "1" ]; then
  PROBES+=("prod settings_public ${PROD_SITE_BASE_URL%/}/api/v1/settings/public")
fi

total=0
ok=0
failures=()

for spec in "${PROBES[@]}"; do
  target="$(printf '%s' "$spec" | awk '{print $1}')"
  check="$(printf '%s' "$spec" | awk '{print $2}')"
  url="$(printf '%s' "$spec" | awk '{print $3}')"
  tmp="$(mktemp /tmp/tk-release-health.XXXXXX)"
  set +e
  curl_out="$(curl -sS --max-time "$CURL_TIMEOUT_SECONDS" -o "$tmp" -w '\n%{http_code} %{time_total}' "$url" 2>&1)"
  curl_rc=$?
  set -e
  metrics="$(printf '%s\n' "$curl_out" | tail -n 1)"
  curl_error="$(printf '%s\n' "$curl_out" | sed '$d' | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
  code="$(printf '%s' "$metrics" | awk '{print $1}')"
  time_total="$(printf '%s' "$metrics" | awk '{print $2}')"
  [ -n "$code" ] || code=000
  [ -n "$time_total" ] || time_total=0
  case "$code" in *[!0-9]*) code=000 ;; esac
  bytes="$(wc -c < "$tmp" | tr -d ' ')"
  rm -f "$tmp"
  status=fail
  if [ "$curl_rc" -eq 0 ] && [ "$code" = "200" ]; then
    status=ok
    ok=$((ok + 1))
  else
    failures+=("${target}:${check}:${code}:curl_rc=${curl_rc}")
  fi
  total=$((total + 1))
  python3 - "$target" "$check" "$url" "$status" "$code" "$curl_rc" "$time_total" "$bytes" "$curl_error" <<'PY'
import json, sys
target, check, url, status, code, curl_rc, time_total, bytes_, curl_error = sys.argv[1:]


def parse_float(value):
    try:
        return float(value)
    except ValueError:
        return 0.0


row = {
    "target": target,
    "check": check,
    "url": url,
    "status": status,
    "http_code": int(code) if code.isdigit() else code,
    "curl_rc": int(curl_rc),
    "time_total": parse_float(time_total) if time_total else 0.0,
    "bytes": int(bytes_) if bytes_.isdigit() else bytes_,
}
if curl_error:
    row["curl_error"] = curl_error
print(json.dumps(row, sort_keys=True))
PY
done

python3 - "$ok" "$total" "${failures[@]+"${failures[@]}"}" <<'PY'
import json, sys
ok = int(sys.argv[1])
total = int(sys.argv[2])
failures = sys.argv[3:]
print(json.dumps({
    "summary": "control_plane",
    "status": "ok" if ok == total else "fail",
    "ok": ok,
    "total": total,
    "failures": failures,
}, sort_keys=True))
PY

[ "$ok" -eq "$total" ] || exit 4
