#!/usr/bin/env bash
# probe-release-control-plane.sh — read-only post-release control-plane health.
#
# Checks prod /health (api.* gateway) + /api/v1/settings/public (apex human
# surface after phase 2) and each deployable Edge /health.
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
#   EDGE_IDS=uk1,us4 limits edges; EDGE_IDS=none checks prod only.
#   MATRIX_DIR=deploy overrides the deployment root for offline matrix fixtures.
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

# shellcheck source=../stage0/smoke_lib.sh
source "${REPO_ROOT}/ops/stage0/smoke_lib.sh"

PROD_BASE_URL="${PROD_BASE_URL:-https://api.tokenkey.dev}"
PROD_SITE_BASE_URL="$(smoke_human_base_url "${PROD_BASE_URL}")"
CURL_TIMEOUT_SECONDS="${CURL_TIMEOUT_SECONDS:-10}"
INCLUDE_SETTINGS_PUBLIC="${INCLUDE_SETTINGS_PUBLIC:-1}"
EDGE_IDS="${EDGE_IDS:-}"
MATRIX_DIR="${MATRIX_DIR:-deploy}"

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

# Enumeration and domain materialization share the live fleet owner. Resolve
# every selected target before probing, retaining failures in the final summary.
TARGETS="$(python3 - "$EDGE_IDS" "$MATRIX_DIR" <<'PYTARGETS'
import pathlib, sys
sys.path.insert(0, str(pathlib.Path.cwd() / "ops/stage0"))
from edge_routing_matrix import live_deployable_edge_ids, load_hetzner_targets, load_lightsail_targets, resolve_route_tab
selector, matrix_dir = sys.argv[1:]
root = pathlib.Path(matrix_dir).resolve().parent
if selector == "none":
    raise SystemExit(0)
try:
    hz = load_hetzner_targets(root)
    ls = load_lightsail_targets(root)
    ids = list(dict.fromkeys(selector.replace(",", " ").split())) if selector else live_deployable_edge_ids(root)
except (ValueError, OSError, SystemExit) as exc:
    print(f"fleet target resolution failed: {exc}", file=sys.stderr)
    print("fleet target_resolution -")
    raise SystemExit(0)
for eid in ids:
    # Whitespace-delimited shell records require matrix identities and URLs
    # without whitespace; never let malformed input change record structure.
    label = "edge:" + eid
    try:
        platform, _, _ = resolve_route_tab(root, eid)
        domain = (hz if platform == "hetzner" else ls)[eid]["domain"]
        if not isinstance(domain, str) or not domain or any(c.isspace() for c in domain):
            raise ValueError("invalid edge domain")
        print(f"{label} health https://{domain}/health")
    except (KeyError, ValueError, SystemExit) as exc:
        print(f"{label} target resolution failed: {exc}", file=sys.stderr)
        print(f"{label} target_resolution -")
PYTARGETS
)"
while IFS= read -r target; do
  [ -z "$target" ] || PROBES+=("$target")
done <<< "$TARGETS"

total=0
ok=0
failures=()

for spec in "${PROBES[@]}"; do
  target="$(printf '%s' "$spec" | awk '{print $1}')"
  check="$(printf '%s' "$spec" | awk '{print $2}')"
  url="$(printf '%s' "$spec" | awk '{print $3}')"
  if [ "$check" = target_resolution ]; then
    failures+=("${target}:target_resolution")
    total=$((total + 1))
    python3 - "$target" <<'PYERROR'
import json, sys
print(json.dumps({"target": sys.argv[1], "check": "target_resolution",
                  "status": "fail", "error": "live target resolution failed"}, sort_keys=True))
PYERROR
    continue
  fi
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
