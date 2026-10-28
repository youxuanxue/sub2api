#!/usr/bin/env bash
# Bundle: cn-prod host monitor + Recipe-1 client.
# Live load requires APPROVE=yes-formal-tpm-loadtest. prepare and dry-run send no client traffic.
#
# Pool perf (kimi-k3, ~50k in / ~1k out, streaming). Each concurrency holds 300s.
# No traffic:
#   bash ops/observability/tpm_burst_bundle.sh perf-prepare
#   bash ops/observability/tpm_burst_bundle.sh perf-dry-run
# After approval, key must be bound to china_deployed:
#   APPROVE=yes-formal-tpm-loadtest TOKENKEY_API_KEY="$TOKENKEY_API_KEY" \
#     bash ops/observability/tpm_burst_bundle.sh perf-run
#
# Legacy TPM burst (needs calibrate JSON + prompt file):
#   bash ops/observability/tpm_burst_bundle.sh prepare
#   bash ops/observability/tpm_burst_bundle.sh dry-run
#   APPROVE=yes-formal-tpm-loadtest bash ops/observability/tpm_burst_bundle.sh run
#
# Default BASE_URL=https://api.galaxywaveai.com
# Do NOT use https://api-cn.tokenkey.dev — that DNS points at Tencent USW prod.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

CMD="${1:-}"
shift || true

CALIBRATE_JSON="${CALIBRATE_JSON:-/tmp/tpm-recipe1/calibrate-repo-explain-n5.json}"
PROMPT_FILE="${PROMPT_FILE:-/tmp/tpm-recipe1/sub2api-explain-prompt.txt}"
OUT_DIR="${OUT_DIR:-/tmp/tpm-recipe1/formal-$(date +%Y%m%d-%H%M%S)}"
BASE_URL="${TOKENKEY_BASE_URL:-https://api.galaxywaveai.com}"
HOST_HEADER="${TOKENKEY_HOST_HEADER:-}"
MODEL="${MODEL:-kimi-k3}"
MAX_TOKENS="${MAX_TOKENS:-1200}"
# Empty CASES_PATH uses the owned ~50k filler. Set it to replay a customer jsonl instead.
CASES_PATH="${CASES_PATH:-}"
PERF_PROMPT_TOKENS_TARGET="${PERF_PROMPT_TOKENS_TARGET:-50000}"
PERF_MAX_TOKENS="${PERF_MAX_TOKENS:-1000}"
PERF_STEPS="${PERF_STEPS:-4,8,16,32}"
PERF_HOLD_S="${PERF_HOLD_S:-300}"
SUCCESS_MIN="${SUCCESS_MIN:-0.98}"
PERF_MONITOR_DURATION_S="${PERF_MONITOR_DURATION_S:-1800}"
MONITOR_DURATION_S="${MONITOR_DURATION_S:-240}"
MONITOR_INTERVAL_S="${MONITOR_INTERVAL_S:-2}"
EXPECTED_INSTANCE_ID="${EXPECTED_INSTANCE_ID:-lhins-6qmb3fd6}"
# Live capacity: accounts × per-account concurrency (not the recipe's 21×12 snapshot).
POOL_SLOTS="${TPM_BURST_POOL_SLOTS:-${POOL_SLOTS:-}}"
APPROVE_TOKEN="yes-formal-tpm-loadtest"

usage() {
  sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'
  cat <<EOF

Env overrides:
  CALIBRATE_JSON PROMPT_FILE OUT_DIR TOKENKEY_API_KEY TOKENKEY_BASE_URL
  TOKENKEY_HOST_HEADER (only when BASE_URL is an IP; leave empty for ${BASE_URL})
  MODEL MAX_TOKENS MONITOR_DURATION_S MONITOR_INTERVAL_S EXPECTED_INSTANCE_ID
  TOTAL_REQUESTS MAX_INFLIGHT APPROVE
  CASES_PATH PERF_PROMPT_TOKENS_TARGET PERF_MAX_TOKENS PERF_STEPS PERF_HOLD_S SUCCESS_MIN
  PERF_MONITOR_DURATION_S
  TPM_BURST_POOL_SLOTS|POOL_SLOTS  (accounts×concurrency; caps perf steps)

Default BASE_URL=https://api.galaxywaveai.com
Do NOT use https://api-cn.tokenkey.dev — that DNS points at Tencent USW prod.
EOF
}

pool_slot_args() {
  if [ -n "${POOL_SLOTS}" ]; then
    printf '%s\n' --pool-slots "$POOL_SLOTS"
  fi
}

host_args() {
  if [ -n "$HOST_HEADER" ]; then
    printf '%s\n' --host-header "$HOST_HEADER"
  fi
}

perf_shape_args() {
  if [ -n "$CASES_PATH" ]; then
    printf '%s\n' --cases "$CASES_PATH"
  else
    printf '%s\n' --prompt-tokens-target "$PERF_PROMPT_TOKENS_TARGET" --max-tokens "$PERF_MAX_TOKENS"
  fi
}

refuse_aws_domain_base_url() {
  case "$BASE_URL" in
    *api-cn.tokenkey.dev*)
      echo "{\"phase\":\"bundle_refused\",\"error\":\"BASE_URL=$BASE_URL uses overseas-prod DNS; use https://api.galaxywaveai.com\"}" >&2
      exit 2
      ;;
  esac
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
  refuse_aws_domain_base_url
  mkdir -p "$OUT_DIR"
  # shellcheck disable=SC2046
  echo "=== plan ==="
  python3 ops/observability/tpm_burst_recipe1.py plan --from "$CALIBRATE_JSON" \
    $(pool_slot_args) \
    | tee "$OUT_DIR/plan.json"
  echo
  echo "=== formal dry-run preview ==="
  # shellcheck disable=SC2046
  python3 ops/observability/tpm_burst_recipe1.py formal \
    --from "$CALIBRATE_JSON" \
    --prompt-file "$PROMPT_FILE" \
    --base-url "$BASE_URL" \
    $(host_args) \
    --model "$MODEL" \
    --max-tokens "$MAX_TOKENS" \
    $(pool_slot_args) \
    --dry-run \
    | tee "$OUT_DIR/formal-dry-run.json"
  cat <<EOF

=== ready; waiting for human approve ===
Artifacts: $OUT_DIR
Prompt:    $PROMPT_FILE
Calibrate: $CALIBRATE_JSON
PoolSlots: ${POOL_SLOTS:-"(legacy snapshot 21×12 — set TPM_BURST_POOL_SLOTS=accounts×concurrency)"}

When you approve formal traffic, run:

  APPROVE=${APPROVE_TOKEN} \\
  TOKENKEY_API_KEY="\$TOKENKEY_API_KEY" \\
  TPM_BURST_POOL_SLOTS=1464 \\
  bash ops/observability/tpm_burst_bundle.sh run

Optional overrides on run:
  TOTAL_REQUESTS=193 MAX_INFLIGHT=157 MONITOR_DURATION_S=240 TPM_BURST_POOL_SLOTS=1464
EOF
}

cmd_dry_run() {
  need_files
  refuse_aws_domain_base_url
  mkdir -p "$OUT_DIR"
  echo "=== client dry-run (no traffic) ==="
  # shellcheck disable=SC2046
  python3 ops/observability/tpm_burst_recipe1.py formal \
    --from "$CALIBRATE_JSON" \
    --prompt-file "$PROMPT_FILE" \
    --base-url "$BASE_URL" \
    $(host_args) \
    --model "$MODEL" \
    --max-tokens "$MAX_TOKENS" \
    $(pool_slot_args) \
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
  refuse_aws_domain_base_url
  if [ "${APPROVE:-}" != "$APPROVE_TOKEN" ]; then
    echo "{\"phase\":\"bundle_blocked\",\"need\":\"APPROVE=$APPROVE_TOKEN\"}" >&2
    exit 10
  fi
  if [ -z "${TOKENKEY_API_KEY:-}${OPENAI_API_KEY:-}" ]; then
    echo "TOKENKEY_API_KEY (or OPENAI_API_KEY) required" >&2
    exit 2
  fi
  mkdir -p "$OUT_DIR"
  echo "target_base_url=$BASE_URL host_header=$HOST_HEADER"

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
  # shellcheck disable=SC2046
  # "${TOTAL_ARGS[@]+"${TOTAL_ARGS[@]}"}" is set -u safe when the array is empty.
  APPROVE="$APPROVE_TOKEN" python3 ops/observability/tpm_burst_recipe1.py formal \
    --from "$CALIBRATE_JSON" \
    --prompt-file "$PROMPT_FILE" \
    --base-url "$BASE_URL" \
    $(host_args) \
    --api-key "${TOKENKEY_API_KEY:-${OPENAI_API_KEY:-}}" \
    --model "$MODEL" \
    --max-tokens "$MAX_TOKENS" \
    --out "$OUT_DIR/formal-summary.json" \
    $(pool_slot_args) \
    ${TOTAL_ARGS[@]+"${TOTAL_ARGS[@]}"} \
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

cmd_perf_prepare() {
  refuse_aws_domain_base_url
  mkdir -p "$OUT_DIR"
  # shellcheck disable=SC2046
  python3 ops/observability/tpm_burst_recipe1.py perf \
    --base-url "$BASE_URL" \
    --model "$MODEL" \
    $(perf_shape_args) \
    --steps "$PERF_STEPS" \
    --hold-s "$PERF_HOLD_S" \
    --success-min "$SUCCESS_MIN" \
    $(host_args) \
    $(pool_slot_args) \
    --dry-run \
    | tee "$OUT_DIR/perf-plan.json"
  cat <<EOF

=== ready; waiting for human approve ===
No client traffic was sent.
Artifacts: $OUT_DIR/perf-plan.json
Entrance:  $BASE_URL
Shape:     owned context prompt_tokens_target=$PERF_PROMPT_TOKENS_TARGET max_tokens=$PERF_MAX_TOKENS
Cases:     ${CASES_PATH:-<unset; synthetic context>}
Steps:     $PERF_STEPS ; each concurrency holds ${PERF_HOLD_S}s, stop below success_min=$SUCCESS_MIN
Key:       must be bound to china_deployed

When you approve:

  APPROVE=${APPROVE_TOKEN} \\
  TOKENKEY_API_KEY="\$TOKENKEY_API_KEY" \\
  bash ops/observability/tpm_burst_bundle.sh perf-run
EOF
}

cmd_perf_dry_run() {
  # Local plan only. Does not SSM and does not send chat traffic.
  cmd_perf_prepare
  echo "perf dry-run complete; no formal load sent"
}

cmd_perf_run() {
  refuse_aws_domain_base_url
  if [ "${APPROVE:-}" != "$APPROVE_TOKEN" ]; then
    echo "{\"phase\":\"bundle_blocked\",\"need\":\"APPROVE=$APPROVE_TOKEN\",\"will_not_send_traffic\":true}" >&2
    exit 10
  fi
  if [ -z "${TOKENKEY_API_KEY:-}${OPENAI_API_KEY:-}" ]; then
    echo "TOKENKEY_API_KEY (or OPENAI_API_KEY) required" >&2
    exit 2
  fi
  mkdir -p "$OUT_DIR"
  echo "target_base_url=$BASE_URL host_header=${HOST_HEADER:-<none>}"

  echo "=== start remote monitor (background) duration=${PERF_MONITOR_DURATION_S}s ==="
  bash ops/observability/run-probe.sh \
    --target cn-prod \
    --script ops/observability/tpm_burst_monitor_remote.sh \
    --expected-instance-id "$EXPECTED_INSTANCE_ID" \
    --env DURATION_S="$PERF_MONITOR_DURATION_S" \
    --env INTERVAL_S="$MONITOR_INTERVAL_S" \
    --comment "pool-perf formal monitor" \
    --timeout-seconds $((PERF_MONITOR_DURATION_S + 120)) \
    --compressed-output \
    >"$OUT_DIR/monitor.out" 2>"$OUT_DIR/monitor.err" &
  MON_PID=$!
  echo "monitor_pid=$MON_PID"
  sleep 3

  echo "=== start pool perf client ==="
  set +e
  # shellcheck disable=SC2046
  APPROVE="$APPROVE_TOKEN" python3 ops/observability/tpm_burst_recipe1.py perf \
    --base-url "$BASE_URL" \
    --model "$MODEL" \
    $(perf_shape_args) \
    --steps "$PERF_STEPS" \
    --hold-s "$PERF_HOLD_S" \
    --success-min "$SUCCESS_MIN" \
    $(host_args) \
    $(pool_slot_args) \
    --out "$OUT_DIR/perf-summary.json" \
    --requests-out "$OUT_DIR/requests.jsonl" \
    >"$OUT_DIR/perf.out" 2>"$OUT_DIR/perf.err"
  LOAD_RC=$?
  set -e
  echo "perf_exit=$LOAD_RC"
  cat "$OUT_DIR/perf.out"

  echo "=== wait for monitor pid=$MON_PID ==="
  set +e
  wait "$MON_PID"
  MON_RC=$?
  set -e
  echo "monitor_exit=$MON_RC"

  python3 - <<PY
import json
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
  "perf_exit": $LOAD_RC,
  "monitor_exit": $MON_RC,
  "monitor_summary": summary,
  "perf_summary_path": str(out / "perf-summary.json"),
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
  perf-prepare) cmd_perf_prepare "$@" ;;
  perf-dry-run) cmd_perf_dry_run "$@" ;;
  perf-run) cmd_perf_run "$@" ;;
  -h|--help|"") usage; exit 0 ;;
  *) echo "unknown command: $CMD" >&2; usage; exit 2 ;;
esac
