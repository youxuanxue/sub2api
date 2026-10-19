#!/usr/bin/env bash
# Bundle: host monitor (cn-prod) + formal TPM burst client.
# Formal traffic requires APPROVE=yes-formal-tpm-loadtest.
#
# Usage:
#   bash ops/observability/tpm_burst_bundle.sh prepare
#   bash ops/observability/tpm_burst_bundle.sh dry-run
#   APPROVE=yes-formal-tpm-loadtest bash ops/observability/tpm_burst_bundle.sh run
#
# Defaults point at the latest repo-explain calibration artifacts under /tmp/tpm-recipe1.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

CMD="${1:-}"
shift || true

CALIBRATE_JSON="${CALIBRATE_JSON:-/tmp/tpm-recipe1/calibrate-repo-explain-n5.json}"
PROMPT_FILE="${PROMPT_FILE:-/tmp/tpm-recipe1/sub2api-explain-prompt.txt}"
OUT_DIR="${OUT_DIR:-/tmp/tpm-recipe1/formal-$(date +%Y%m%d-%H%M%S)}"
BASE_URL="${TOKENKEY_BASE_URL:-https://api-cn.tokenkey.dev}"
MODEL="${MODEL:-kimi-k3}"
MAX_TOKENS="${MAX_TOKENS:-1200}"
MONITOR_DURATION_S="${MONITOR_DURATION_S:-240}"
MONITOR_INTERVAL_S="${MONITOR_INTERVAL_S:-2}"
EXPECTED_INSTANCE_ID="${EXPECTED_INSTANCE_ID:-uhost-1wgyn7gu2f6a}"
APPROVE_TOKEN="yes-formal-tpm-loadtest"

usage() {
  sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
  cat <<EOF

Env overrides:
  CALIBRATE_JSON PROMPT_FILE OUT_DIR TOKENKEY_API_KEY TOKENKEY_BASE_URL
  MODEL MAX_TOKENS MONITOR_DURATION_S MONITOR_INTERVAL_S EXPECTED_INSTANCE_ID
  TOTAL_REQUESTS MAX_INFLIGHT APPROVE
EOF
}

need_files() {
  if [ ! -f "$CALIBRATE_JSON" ]; then
    echo "missing CALIBRATE_JSON=$CALIBRATE_JSON" >&2
    exit 2
  fi
  if [ ! -f "$PROMPT_FILE" ]; then
    echo "missing PROMPT_FILE=$PROMPT_FILE (run ops/observability/pack_repo_prompt.py first)" >&2
    exit 2
  fi
}

cmd_prepare() {
  need_files
  mkdir -p "$OUT_DIR"
  echo "=== plan ==="
  python3 ops/observability/tpm_burst_recipe1.py plan --from "$CALIBRATE_JSON" \
    | tee "$OUT_DIR/plan.json"
  echo
  echo "=== formal dry-run preview ==="
  python3 ops/observability/tpm_burst_recipe1.py formal \
    --from "$CALIBRATE_JSON" \
    --prompt-file "$PROMPT_FILE" \
    --base-url "$BASE_URL" \
    --model "$MODEL" \
    --max-tokens "$MAX_TOKENS" \
    --dry-run \
    | tee "$OUT_DIR/formal-dry-run.json"
  cat <<EOF

=== ready; waiting for human approve ===
Artifacts: $OUT_DIR
Prompt:    $PROMPT_FILE
Calibrate: $CALIBRATE_JSON

When you approve formal traffic, run:

  APPROVE=${APPROVE_TOKEN} \\
  TOKENKEY_API_KEY="\$TOKENKEY_API_KEY" \\
  bash ops/observability/tpm_burst_bundle.sh run

Optional overrides on run:
  TOTAL_REQUESTS=193 MAX_INFLIGHT=157 MONITOR_DURATION_S=240
EOF
}

cmd_dry_run() {
  need_files
  mkdir -p "$OUT_DIR"
  echo "=== client dry-run (no traffic) ==="
  python3 ops/observability/tpm_burst_recipe1.py formal \
    --from "$CALIBRATE_JSON" \
    --prompt-file "$PROMPT_FILE" \
    --base-url "$BASE_URL" \
    --model "$MODEL" \
    --max-tokens "$MAX_TOKENS" \
    --dry-run \
    | tee "$OUT_DIR/formal-dry-run.json"

  echo
  echo "=== monitor smoke (cn-prod, 8s) ==="
  bash ops/observability/run-probe.sh \
    --target cn-prod \
    --script ops/observability/tpm_burst_monitor_remote.sh \
    --expected-instance-id "$EXPECTED_INSTANCE_ID" \
    --env DURATION_S=8 \
    --env INTERVAL_S=2 \
    --comment "tpm-burst monitor smoke" \
    --timeout-seconds 60 \
    --compressed-output \
    | tee "$OUT_DIR/monitor-smoke.out"
  echo
  echo "dry-run complete; no formal load sent"
}

cmd_run() {
  need_files
  if [ "${APPROVE:-}" != "$APPROVE_TOKEN" ]; then
    echo "{\"phase\":\"bundle_blocked\",\"need\":\"APPROVE=$APPROVE_TOKEN\"}" >&2
    exit 10
  fi
  if [ -z "${TOKENKEY_API_KEY:-}${OPENAI_API_KEY:-}" ]; then
    echo "TOKENKEY_API_KEY (or OPENAI_API_KEY) required" >&2
    exit 2
  fi
  mkdir -p "$OUT_DIR"

  TOTAL_ARGS=()
  if [ -n "${TOTAL_REQUESTS:-}" ]; then TOTAL_ARGS+=(--total-requests "$TOTAL_REQUESTS"); fi
  if [ -n "${MAX_INFLIGHT:-}" ]; then TOTAL_ARGS+=(--max-inflight "$MAX_INFLIGHT"); fi

  echo "=== start remote monitor (background) duration=${MONITOR_DURATION_S}s ==="
  # shellcheck disable=SC2086
  bash ops/observability/run-probe.sh \
    --target cn-prod \
    --script ops/observability/tpm_burst_monitor_remote.sh \
    --expected-instance-id "$EXPECTED_INSTANCE_ID" \
    --env DURATION_S="$MONITOR_DURATION_S" \
    --env INTERVAL_S="$MONITOR_INTERVAL_S" \
    --comment "tpm-burst formal monitor" \
    --timeout-seconds $((MONITOR_DURATION_S + 120)) \
    --compressed-output \
    >"$OUT_DIR/monitor.out" 2>"$OUT_DIR/monitor.err" &
  MON_PID=$!
  echo "monitor_pid=$MON_PID"
  # brief settle so first samples land before load
  sleep 3

  echo "=== start formal client load ==="
  set +e
  APPROVE="$APPROVE_TOKEN" python3 ops/observability/tpm_burst_recipe1.py formal \
    --from "$CALIBRATE_JSON" \
    --prompt-file "$PROMPT_FILE" \
    --base-url "$BASE_URL" \
    --api-key "${TOKENKEY_API_KEY:-${OPENAI_API_KEY:-}}" \
    --model "$MODEL" \
    --max-tokens "$MAX_TOKENS" \
    --out "$OUT_DIR/formal-summary.json" \
    "${TOTAL_ARGS[@]}" \
    2>"$OUT_DIR/formal.err" | tee "$OUT_DIR/formal.out"
  LOAD_RC=${PIPESTATUS[0]}
  set -e
  echo "formal_exit=$LOAD_RC"

  echo "=== wait for monitor pid=$MON_PID ==="
  set +e
  wait "$MON_PID"
  MON_RC=$?
  set -e
  echo "monitor_exit=$MON_RC"

  python3 - <<PY
import json, re
from pathlib import Path
out = Path("$OUT_DIR")
mon = (out / "monitor.out").read_text(errors="replace").splitlines()
summary = None
for line in reversed(mon):
    line=line.strip()
    if not line.startswith("{"):
        continue
    try:
        o=json.loads(line)
    except Exception:
        continue
    if o.get("phase")=="monitor_summary":
        summary=o
        break
bundle = {
  "phase": "bundle_complete",
  "out_dir": str(out),
  "formal_exit": $LOAD_RC,
  "monitor_exit": $MON_RC,
  "monitor_summary": summary,
  "formal_summary_path": str(out / "formal-summary.json"),
  "next": "query cn-prod usage_logs for rolling-60s peak TPM; compare to monitor_summary peaks",
}
print(json.dumps(bundle, ensure_ascii=False, indent=2))
(out / "bundle-summary.json").write_text(json.dumps(bundle, ensure_ascii=False, indent=2) + "\n")
PY

  exit "$LOAD_RC"
}

case "$CMD" in
  prepare) cmd_prepare "$@" ;;
  dry-run) cmd_dry_run "$@" ;;
  run) cmd_run "$@" ;;
  -h|--help|"") usage; exit 0 ;;
  *) echo "unknown command: $CMD" >&2; usage; exit 2 ;;
esac
