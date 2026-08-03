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
printf '%s\n' 'mock probe payload'
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

echo "test_scan_edge_health: ok"
