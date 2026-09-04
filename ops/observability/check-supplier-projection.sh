#!/usr/bin/env bash
# Run the prod supplier projection probe and preserve checker-style exit codes:
# 0 aligned, 1 drift, 2 setup/transport failure.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TARGET="prod"
EXPECTED_INSTANCE_ID=""
TIMEOUT_SECONDS="180"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --target) TARGET="${2:-}"; shift 2 ;;
    --expected-instance-id) EXPECTED_INSTANCE_ID="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    -h|--help)
      echo "usage: $0 [--target prod] [--expected-instance-id i-*] [--timeout-seconds 180]"
      exit 0
      ;;
    *) echo "[check-supplier-projection] unknown arg: $1" >&2; exit 2 ;;
  esac
done

REPORT_FILE="$(mktemp /tmp/tk-supplier-projection.XXXXXX)"
trap 'rm -f "$REPORT_FILE"' EXIT
RUN_PROBE="${SUPPLIER_PROJECTION_RUN_PROBE:-$ROOT/ops/observability/run-probe.sh}"
ARGS=(
  --target "$TARGET"
  --script "$ROOT/ops/observability/probe-supplier-projection.sh"
  --timeout-seconds "$TIMEOUT_SECONDS"
)
if [ -n "$EXPECTED_INSTANCE_ID" ]; then
  ARGS+=(--expected-instance-id "$EXPECTED_INSTANCE_ID")
fi

set +e
bash "$RUN_PROBE" "${ARGS[@]}" | tee "$REPORT_FILE"
PROBE_RC=${PIPESTATUS[0]}
set -e

VERDICT="$(python3 - "$REPORT_FILE" <<'PY'
import json
import sys

verdict = ""
with open(sys.argv[1], encoding="utf-8") as handle:
    for raw in handle:
        try:
            row = json.loads(raw)
        except json.JSONDecodeError:
            continue
        if isinstance(row, dict) and row.get("verdict"):
            verdict = str(row["verdict"])
print(verdict)
PY
)"

case "$VERDICT" in
  aligned)
    [ "$PROBE_RC" -eq 0 ] || exit 2
    exit 0
    ;;
  drift) exit 1 ;;
  setup_error) exit 2 ;;
  *)
    echo "[check-supplier-projection] no valid verdict (probe rc=$PROBE_RC)" >&2
    exit 2
    ;;
esac
