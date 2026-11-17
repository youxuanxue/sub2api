#!/usr/bin/env bash
# Historical local driver (2026-11-17 200/400 formal run): sources TK_FULLTEST_KEY
# and SSM-delivers pool perf onto cn-prod **app-a** (same host as the gateway).
#
# Prefer the dedicated Beijing loadtest CVM for new capacity runs (especially
# 600/800): app-host client contaminates CPU/loadavg and contradicts
# docs/decisions/decided.md + deploy/domestic/README.md (ins-7ltl2zv3,
# expire_after=2026-11-19). Keep this script for replaying the app-host method
# only. Never echo the key. Live traffic requires APPROVE below.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

if [ "${ALLOW_APP_HOST_PERF_CLIENT:-}" != "yes" ]; then
  echo '{"phase":"loadtest_blocked","error":"app-host client discouraged after 2026-11-17; use dedicated loadtest CVM, or set ALLOW_APP_HOST_PERF_CLIENT=yes to override","will_not_send_traffic":true}' >&2
  exit 2
fi

# Pull only the key via non-interactive zsh — bash `source ~/.zshrc` can hang
# on zsh-only startup hooks. Never print the value.
if [ -z "${TK_FULLTEST_KEY:-}" ]; then
  TK_FULLTEST_KEY="$(zsh -fc 'source ~/.zshrc >/dev/null 2>&1; print -r -- ${TK_FULLTEST_KEY:-}')"
  export TK_FULLTEST_KEY
fi
if [ -z "${TK_FULLTEST_KEY:-}" ]; then
  echo '{"phase":"setup_error","error":"TK_FULLTEST_KEY missing after zsh load of ~/.zshrc"}' >&2
  exit 2
fi

OUT_DIR="${OUT_DIR:-/tmp/tpm-recipe1/cn-prod-remote-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT_DIR"
echo "local_out_dir=$OUT_DIR"
echo "key_present=1 key_len=${#TK_FULLTEST_KEY}"

# ~10 min holds (200+400) + calibrate/drain + monitor slack
TIMEOUT_S="${TIMEOUT_S:-1500}"

set +e
bash ops/observability/run-probe.sh \
  --target cn-prod \
  --script ops/observability/tpm_burst_perf_remote.sh \
  --with ops/observability/tpm_burst_recipe1.py \
  --with ops/observability/tpm_burst_monitor_remote.sh \
  --expected-instance-id lhins-6qmb3fd6 \
  --comment "cn-prod deepseek-flash pool perf from app host" \
  --timeout-seconds "$TIMEOUT_S" \
  --compressed-output \
  --env APPROVE=yes-formal-tpm-loadtest \
  --env "TOKENKEY_API_KEY=${TK_FULLTEST_KEY}" \
  --env MODEL=deepseek-flash \
  --env PERF_PROMPT_TOKENS_TARGET=10000 \
  --env PERF_MAX_TOKENS=1000 \
  --env PERF_STEPS=200,400 \
  --env PERF_HOLD_S=300 \
  --env POOL_SLOTS=1045 \
  --env PERF_MONITOR_DURATION_S=900 \
  --env MONITOR_INTERVAL_S=2 \
  --env MONITOR=1 \
  --env TOKENKEY_BASE_URL=https://api.galaxywaveai.com \
  >"$OUT_DIR/run-probe.out" 2>"$OUT_DIR/run-probe.err"
RC=$?
set -e

echo "run_probe_exit=$RC"
# Show non-secret status lines only
grep -E '^\[run-probe\]|^\{\"phase\"|^=== |remote_perf|perf_exit|monitor_exit|operating_point|success_rate|rpm_|tpm_|concurrency_' \
  "$OUT_DIR/run-probe.out" "$OUT_DIR/run-probe.err" 2>/dev/null | head -n 200 || true

# Persist a small local pointer (no secrets)
python3 - <<PY
import json
from pathlib import Path
out = Path("$OUT_DIR")
print(json.dumps({
  "phase": "local_driver_complete",
  "run_probe_exit": $RC,
  "out_dir": str(out),
  "stdout_bytes": (out / "run-probe.out").stat().st_size if (out / "run-probe.out").exists() else 0,
  "stderr_bytes": (out / "run-probe.err").stat().st_size if (out / "run-probe.err").exists() else 0,
}, ensure_ascii=False))
PY
exit "$RC"
