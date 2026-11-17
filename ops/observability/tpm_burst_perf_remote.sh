#!/usr/bin/env bash
# Run Recipe-1 pool perf on a cn-prod app host (delivered via run-probe).
# Avoids operator-laptop uplink limits. Live traffic requires
# APPROVE=yes-formal-tpm-loadtest and TOKENKEY_API_KEY in the remote env.
#
# Companions (run-probe --with):
#   tpm_burst_recipe1.py  → /tmp/tpm_burst_recipe1.py
# Optional monitor stays on this same host in the background.
set -euo pipefail

APPROVE_TOKEN="yes-formal-tpm-loadtest"
if [ "${APPROVE:-}" != "$APPROVE_TOKEN" ]; then
  echo '{"phase":"loadtest_blocked","error":"APPROVE=yes-formal-tpm-loadtest required","will_not_send_traffic":true}' >&2
  exit 10
fi
if [ -z "${TOKENKEY_API_KEY:-}${OPENAI_API_KEY:-}" ]; then
  echo '{"phase":"setup_error","error":"TOKENKEY_API_KEY required"}' >&2
  exit 2
fi

RECIPE="${RECIPE_PATH:-/tmp/tpm_burst_recipe1.py}"
if [ ! -f "$RECIPE" ]; then
  echo "{\"phase\":\"setup_error\",\"error\":\"missing recipe at $RECIPE\"}" >&2
  exit 2
fi

BASE_URL="${TOKENKEY_BASE_URL:-https://api.galaxywaveai.com}"
case "$BASE_URL" in
  *api-cn.tokenkey.dev*)
    echo '{"phase":"bundle_refused","error":"overseas-prod DNS; use https://api.galaxywaveai.com"}' >&2
    exit 2
    ;;
esac

MODEL="${MODEL:-deepseek-flash}"
PERF_PROMPT_TOKENS_TARGET="${PERF_PROMPT_TOKENS_TARGET:-10000}"
PERF_MAX_TOKENS="${PERF_MAX_TOKENS:-1000}"
PERF_STEPS="${PERF_STEPS:-200,400}"
PERF_HOLD_S="${PERF_HOLD_S:-300}"
SUCCESS_MIN="${SUCCESS_MIN:-0.98}"
POOL_SLOTS="${POOL_SLOTS:-${TPM_BURST_POOL_SLOTS:-}}"
OUT_DIR="${OUT_DIR:-/tmp/tpm-recipe1/remote-perf-$(date +%Y%m%d-%H%M%S)}"
MONITOR="${MONITOR:-1}"
PERF_MONITOR_DURATION_S="${PERF_MONITOR_DURATION_S:-900}"
MONITOR_INTERVAL_S="${MONITOR_INTERVAL_S:-2}"
HOST_HEADER="${TOKENKEY_HOST_HEADER:-}"

mkdir -p "$OUT_DIR"

echo "{\"phase\":\"remote_perf_start\",\"host\":\"$(hostname)\",\"base_url\":\"$BASE_URL\",\"model\":\"$MODEL\",\"steps\":\"$PERF_STEPS\",\"hold_s\":$PERF_HOLD_S,\"pool_slots\":${POOL_SLOTS:-null},\"out_dir\":\"$OUT_DIR\",\"monitor\":$MONITOR}"

MON_PID=""
if [ "$MONITOR" = "1" ] && [ -f /tmp/tpm_burst_monitor_remote.sh ]; then
  DURATION_S="$PERF_MONITOR_DURATION_S" INTERVAL_S="$MONITOR_INTERVAL_S" \
    bash /tmp/tpm_burst_monitor_remote.sh \
    >"$OUT_DIR/monitor.out" 2>"$OUT_DIR/monitor.err" &
  MON_PID=$!
  echo "{\"phase\":\"monitor_started\",\"pid\":$MON_PID,\"duration_s\":$PERF_MONITOR_DURATION_S}"
  sleep 2
fi

ARGS=(
  perf
  --base-url "$BASE_URL"
  --model "$MODEL"
  --prompt-tokens-target "$PERF_PROMPT_TOKENS_TARGET"
  --max-tokens "$PERF_MAX_TOKENS"
  --steps "$PERF_STEPS"
  --hold-s "$PERF_HOLD_S"
  --success-min "$SUCCESS_MIN"
  --out "$OUT_DIR/perf-summary.json"
  --requests-out "$OUT_DIR/requests.jsonl"
)
if [ -n "$POOL_SLOTS" ]; then
  ARGS+=(--pool-slots "$POOL_SLOTS")
fi
if [ -n "$HOST_HEADER" ]; then
  ARGS+=(--host-header "$HOST_HEADER")
fi

set +e
APPROVE="$APPROVE_TOKEN" \
TOKENKEY_API_KEY="${TOKENKEY_API_KEY:-${OPENAI_API_KEY:-}}" \
  python3 "$RECIPE" "${ARGS[@]}" \
  >"$OUT_DIR/perf.out" 2>"$OUT_DIR/perf.err"
LOAD_RC=$?
set -e

echo "{\"phase\":\"perf_finished\",\"exit\":$LOAD_RC}"
# Summary only — never dump requests.jsonl (may be huge); redact wrapper strips keys.
if [ -f "$OUT_DIR/perf-summary.json" ]; then
  echo "=== perf_summary ==="
  cat "$OUT_DIR/perf-summary.json"
fi
if [ -f "$OUT_DIR/perf.out" ]; then
  echo "=== perf_stdout_tail ==="
  tail -n 80 "$OUT_DIR/perf.out"
fi
if [ -f "$OUT_DIR/perf.err" ] && [ -s "$OUT_DIR/perf.err" ]; then
  echo "=== perf_stderr_tail ==="
  tail -n 40 "$OUT_DIR/perf.err"
fi

MON_RC=0
if [ -n "$MON_PID" ]; then
  set +e
  wait "$MON_PID"
  MON_RC=$?
  set -e
  echo "{\"phase\":\"monitor_finished\",\"exit\":$MON_RC}"
  if [ -f "$OUT_DIR/monitor.out" ]; then
    echo "=== monitor_summary ==="
    # Last monitor_summary line if present
    python3 - "$OUT_DIR/monitor.out" <<'PY'
import json, sys
from pathlib import Path
lines = Path(sys.argv[1]).read_text(errors="replace").splitlines()
summary = None
for line in reversed(lines):
    line = line.strip()
    if not line.startswith("{"):
        continue
    try:
        o = json.loads(line)
    except Exception:
        continue
    if o.get("phase") == "monitor_summary":
        summary = o
        break
print(json.dumps(summary or {"phase": "monitor_summary_missing"}, ensure_ascii=False))
PY
  fi
fi

python3 - <<PY
import json
from pathlib import Path
out = Path("$OUT_DIR")
summary = None
p = out / "perf-summary.json"
if p.exists():
    try:
        summary = json.loads(p.read_text())
    except Exception as exc:
        summary = {"error": type(exc).__name__}
print(json.dumps({
  "phase": "remote_perf_complete",
  "perf_exit": $LOAD_RC,
  "monitor_exit": $MON_RC,
  "out_dir": str(out),
  "perf_summary": summary,
}, ensure_ascii=False))
PY

exit "$LOAD_RC"
