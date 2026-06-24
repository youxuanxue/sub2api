#!/usr/bin/env bash
# probe-release-control-plane.sh — read-only post-release control-plane health.
#
# Checks prod /health + /api/v1/settings/public and each deployable Edge /health.
# This mechanizes the tokenkey-stage0-release-rollout follow-up control-plane
# step so operators do not hand-roll jq/curl loops during a release.
#
# Output:
#   - one JSON object per probe on stdout;
#   - a final SUMMARY JSON object with ok/total/failures.
#
# Env:
#   EDGE_IDS=us3,us4 limits edges; EDGE_IDS=none checks prod only.
#
# Usage:
#   bash ops/observability/probe-release-control-plane.sh
#   EDGE_IDS=none bash ops/observability/probe-release-control-plane.sh
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

PROD_BASE_URL="${PROD_BASE_URL:-https://api.tokenkey.dev}"
CURL_TIMEOUT_SECONDS="${CURL_TIMEOUT_SECONDS:-10}"
INCLUDE_SETTINGS_PUBLIC="${INCLUDE_SETTINGS_PUBLIC:-1}"
EDGE_IDS="${EDGE_IDS:-}"
MATRIX_DIR="${MATRIX_DIR:-deploy/aws}"

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
  PROBES+=("prod settings_public ${PROD_BASE_URL%/}/api/v1/settings/public")
fi

if [ "$EDGE_IDS" = "none" ]; then
  EDGE_LIST=""
elif [ -n "$EDGE_IDS" ]; then
  EDGE_LIST="$EDGE_IDS"
else
  EDGE_LIST="$(python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable)"
fi

while IFS= read -r edge_id; do
  [ -n "$edge_id" ] || continue
  domain="$(python3 - "$edge_id" "$MATRIX_DIR" <<'PY'
import json, pathlib, sys
edge, matrix_dir_arg = sys.argv[1], sys.argv[2]
root = pathlib.Path.cwd()
matrix_dir = pathlib.Path(matrix_dir_arg)
if not matrix_dir.is_absolute():
    matrix_dir = root / matrix_dir
ec2_path = matrix_dir / "stage0/edge-targets.json"
ls_path = matrix_dir / "lightsail/edge-targets-lightsail.json"
domain = ""
if ls_path.is_file():
    data = json.loads(ls_path.read_text(encoding="utf-8"))
    target = (data.get("targets") or {}).get(edge) or {}
    if target.get("deployable") is True:
        domain = str(target.get("domain") or "")
if not domain and ec2_path.is_file():
    data = json.loads(ec2_path.read_text(encoding="utf-8"))
    target = (data.get("targets") or {}).get(edge) or {}
    if target.get("deployable") is True:
        domain = str(target.get("domain") or "")
if not domain:
    raise SystemExit(f"no deployable domain for edge {edge}")
print(domain)
PY
)"
  PROBES+=("edge:${edge_id} health https://${domain}/health")
done <<< "$(printf '%s\n' "$EDGE_LIST" | tr ',' '\n' | xargs -n1 2>/dev/null || true)"

total=0
ok=0
failures=()

for spec in "${PROBES[@]}"; do
  target="$(printf '%s' "$spec" | awk '{print $1}')"
  check="$(printf '%s' "$spec" | awk '{print $2}')"
  url="$(printf '%s' "$spec" | awk '{print $3}')"
  tmp="$(mktemp /tmp/tk-release-health.XXXXXX)"
  set +e
  curl_out="$(curl -sS --max-time "$CURL_TIMEOUT_SECONDS" -o "$tmp" -w '%{http_code} %{time_total}' "$url" 2>&1)"
  curl_rc=$?
  set -e
  code="$(printf '%s' "$curl_out" | awk '{print $1}')"
  time_total="$(printf '%s' "$curl_out" | awk '{print $2}')"
  [ -n "$code" ] || code=000
  [ -n "$time_total" ] || time_total=0
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
  python3 - "$target" "$check" "$url" "$status" "$code" "$curl_rc" "$time_total" "$bytes" <<'PY'
import json, sys
target, check, url, status, code, curl_rc, time_total, bytes_ = sys.argv[1:]
print(json.dumps({
    "target": target,
    "check": check,
    "url": url,
    "status": status,
    "http_code": int(code) if code.isdigit() else code,
    "curl_rc": int(curl_rc),
    "time_total": float(time_total) if time_total else 0.0,
    "bytes": int(bytes_) if bytes_.isdigit() else bytes_,
}, sort_keys=True))
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
