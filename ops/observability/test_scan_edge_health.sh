#!/usr/bin/env bash
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMPDIR_SCAN="$(mktemp -d)"
trap 'rm -rf "${TMPDIR_SCAN}"' EXIT

cat > "${TMPDIR_SCAN}/https-probe" <<'PY'
#!/usr/bin/env python3
import os
import sys

mode = os.environ["MOCK_HTTPS_MODE"]
if mode == "unreachable":
    print('{"reachable":false,"healthy":false,"http_code":0}')
elif mode == "http503":
    print('{"reachable":true,"healthy":false,"http_code":503}')
elif mode == "invalid":
    print("not-json")
elif mode == "failure":
    raise SystemExit(2)
else:
    raise SystemExit(3)
PY
cat > "${TMPDIR_SCAN}/run-probe" <<'SH'
#!/usr/bin/env bash
printf '%s\n' called >> "${MOCK_RUN_PROBE_MARKER}"
if [ "${MOCK_TERMINAL_MODE:-valid}" = "malformed" ]; then
  printf '%s\n' 'TERMINAL_META not-json'
  exit 0
fi
printf '%s\n' \
  'TERMINAL_META {"schema_version":1,"watermark":"2026-08-18T12:06:00Z"}' \
  'TERMINAL_WINDOW {"bucket_start":"2026-08-18T12:00:00Z","heartbeat_minutes":5,"producer_epochs":1,"all_complete":true}' \
  'TERMINAL_FACT {"bucket_start":"2026-08-18T12:00:00Z","group_id":7,"requested_model":"claude-sonnet-4-6","success":90,"final_empty_pool_429":10,"other_error":0}'
SH
cat > "${TMPDIR_SCAN}/verdict" <<'PY'
#!/usr/bin/env python3
import json
import sys

label = sys.argv[sys.argv.index("--label") + 1]
print(json.dumps({"edge": label, "verdict": "healthy"}))
PY
cat > "${TMPDIR_SCAN}/resolve-failure" <<'PY'
#!/usr/bin/env python3
raise SystemExit(2)
PY
chmod +x "${TMPDIR_SCAN}/https-probe" "${TMPDIR_SCAN}/run-probe" \
  "${TMPDIR_SCAN}/verdict" "${TMPDIR_SCAN}/resolve-failure"

run_scan() {
  EDGE_HEALTH_HTTPS_PROBE="${TMPDIR_SCAN}/https-probe" \
  EDGE_HEALTH_RUN_PROBE="${TMPDIR_SCAN}/run-probe" \
  EDGE_HEALTH_VERDICT="${TMPDIR_SCAN}/verdict" \
  MOCK_RUN_PROBE_MARKER="${TMPDIR_SCAN}/run-probe.called" \
  MOCK_HTTPS_MODE="$1" \
    bash "${HERE}/scan-edge-health.sh" --edges us6 --json
}

run_alert_scan() {
  EDGE_HEALTH_HTTPS_PROBE="${TMPDIR_SCAN}/https-probe" \
  EDGE_HEALTH_RUN_PROBE="${TMPDIR_SCAN}/run-probe" \
  MOCK_RUN_PROBE_MARKER="${TMPDIR_SCAN}/run-probe.called" \
  MOCK_HTTPS_MODE="$1" \
  MOCK_TERMINAL_MODE="${2:-valid}" \
    bash "${HERE}/scan-edge-health.sh" --edges us6 --alert-json "${@:3}"
}

rm -f "${TMPDIR_SCAN}/run-probe.called"
out="$(run_scan unreachable)"
grep -q '"verdict":"unreachable"' <<< "${out}"
test ! -e "${TMPDIR_SCAN}/run-probe.called"

rm -f "${TMPDIR_SCAN}/run-probe.called"
out="$(run_scan http503)"
grep -q '"verdict": "healthy"' <<< "${out}"
test -s "${TMPDIR_SCAN}/run-probe.called"

for mode in invalid failure; do
  rm -f "${TMPDIR_SCAN}/run-probe.called"
  if out="$(run_scan "${mode}")"; then
    echo "FAIL: ${mode} HTTPS helper result must fail the scan" >&2
    exit 1
  fi
  grep -q '"verdict": "healthy"' <<< "${out}"
  test -s "${TMPDIR_SCAN}/run-probe.called"
done

if EDGE_HEALTH_RESOLVE="${TMPDIR_SCAN}/resolve-failure" \
  bash "${HERE}/scan-edge-health.sh" --json >/dev/null 2>&1; then
  echo "FAIL: target resolver failure must fail the scan" >&2
  exit 1
fi

out="$(run_alert_scan http503)"
python3 -c '
import json, sys
rows = [json.loads(line) for line in sys.stdin]
assert len(rows) == 1
row = rows[0]
assert row["edge"] == "us6"
assert row["reachable"] is True
assert row["telemetry_status"] in {"fresh", "stale"}
assert row["buckets"][0]["complete"] is True
assert row["buckets"][0]["facts"][0]["requested_model"] == "claude-sonnet-4-6"
' <<< "${out}"

rm -f "${TMPDIR_SCAN}/run-probe.called"
out="$(run_alert_scan unreachable)"
grep -q '"edge":"us6","reachable":false,"reason":"https_unreachable"' <<< "${out}"
test ! -e "${TMPDIR_SCAN}/run-probe.called"

if out="$(run_alert_scan http503 malformed 2>/dev/null)"; then
  echo "FAIL: malformed terminal output must fail the alert scan" >&2
  exit 1
fi
grep -q '"reason":"parse_error"' <<< "${out}"

out="$(run_alert_scan http503 valid --with-prod)"
python3 -c '
import json, sys
rows = [json.loads(line) for line in sys.stdin]
assert [row["edge"] for row in rows] == ["us6", "prod"]
assert all(row["reachable"] is True for row in rows)
' <<< "${out}"

echo "test_scan_edge_health: ok"
