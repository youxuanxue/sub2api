#!/usr/bin/env bash
# Pin one cn-prod NewAPI account via reserved probe group and ladder concurrency.
# Default path replays REQUEST_POOL and keeps each body's output limit.
# PROMPT_TOKENS_TARGET switches to one owned filler; MAX_TOKENS defaults to 1000.
# Forces stream and include_usage. Emits metrics only
# (never prints the probe API key or message text).
# Live traffic requires APPROVE=yes-formal-tpm-loadtest. Default entrance is the
# public cn-prod site, not the in-host loopback.
#
# Env:
#   APPROVE      must be yes-formal-tpm-loadtest
#   ACCOUNT_ID   required numeric account id
#   MODEL        default kimi-k3; overrides the body model when set
#   PROMPT_TOKENS_TARGET  when set, send one owned filler context of about this many
#                         tokens instead of REQUEST_POOL. MAX_TOKENS default 1000.
#   REQUEST_POOL default /tmp/tk-rt-cases-200-bodies.jsonl.gz
#   STEPS        default "5,10,15"
#   HOLD_S       default 300; each concurrency stays full for this many seconds
#   CALIBRATE_N  default 2
#   TIMEOUT_S    default 600
#   BASE_URL     default https://api.galaxywaveai.com
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
MODEL="${MODEL:-kimi-k3}"
REQUEST_POOL="${REQUEST_POOL:-/tmp/tk-rt-cases-200-bodies.jsonl.gz}"
STEPS="${STEPS:-5,10,15}"
HOLD_S="${HOLD_S:-300}"
CALIBRATE_N="${CALIBRATE_N:-2}"
TIMEOUT_S="${TIMEOUT_S:-600}"
export ACCOUNT_ID MODEL REQUEST_POOL STEPS HOLD_S CALIBRATE_N TIMEOUT_S
if [[ -n "${PROMPT_TOKENS_TARGET:-}" ]]; then
  export PROMPT_TOKENS_TARGET
  MAX_TOKENS="${MAX_TOKENS:-1000}"
  export MAX_TOKENS
fi
case "$HOLD_S" in
  ''|*[!0-9]*)
    echo '{"verdict":"setup_error","error":"HOLD_S must be a positive integer"}' >&2
    exit 2
    ;;
esac
if [[ "$HOLD_S" -lt 1 ]]; then
  echo '{"verdict":"setup_error","error":"HOLD_S must be >= 1"}' >&2
  exit 2
fi

if [[ ! "$ACCOUNT_ID" =~ ^[0-9]+$ ]]; then
  echo '{"verdict":"setup_error","error":"ACCOUNT_ID must be numeric"}' >&2
  exit 2
fi
if [[ "${APPROVE:-}" != "yes-formal-tpm-loadtest" ]]; then
  echo '{"phase":"loadtest_blocked","error":"load test requires human approval","need":"APPROVE=yes-formal-tpm-loadtest","will_not_send_traffic":true}' >&2
  exit 10
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# Host docker (cn-prod SSM) usually has no sudo; fall back if needed.
if docker info >/dev/null 2>&1; then
  DOCKER_BIN=(docker)
elif sudo docker info >/dev/null 2>&1; then
  DOCKER_BIN=(sudo docker)
else
  echo '{"verdict":"setup_error","error":"docker unavailable"}' >&2
  exit 2
fi
PSQL_ARRAY=("${DOCKER_BIN[@]}" exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -q -A -t -v ON_ERROR_STOP=1)
export PSQL_ARRAY
TK_DOCKER="${DOCKER_BIN[*]}"
export TK_DOCKER
# When delivered via run-probe, companions land next to the script under /tmp.
for candidate in \
  "$SCRIPT_DIR/probe_reserved_resources.sh" \
  /tmp/probe_reserved_resources.sh \
  "$SCRIPT_DIR/../pricing/probe_reserved_resources.sh"; do
  if [[ -f "$candidate" ]]; then
    # shellcheck disable=SC1090
    source "$candidate"
    break
  fi
done
if ! declare -F tk_probe_prepare_platform_reuse_probe >/dev/null 2>&1; then
  echo '{"verdict":"setup_error","error":"probe_reserved_resources.sh not sourced"}' >&2
  exit 2
fi

# shellcheck disable=SC1091
. /tmp/resolve-app-container.sh
APP_CONTAINER="$(tk_resolve_app_container auto)"
export APP_CONTAINER

BASE_URL="${BASE_URL:-https://api.galaxywaveai.com}"
export BASE_URL

PLATFORM="$("${DOCKER_BIN[@]}" exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c \
  "SELECT platform FROM accounts WHERE id=${ACCOUNT_ID} AND deleted_at IS NULL LIMIT 1;" | tr -d '[:space:]')"
NAME="$("${DOCKER_BIN[@]}" exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c \
  "SELECT name FROM accounts WHERE id=${ACCOUNT_ID} AND deleted_at IS NULL LIMIT 1;" | tr -d '\n')"
CONC="$("${DOCKER_BIN[@]}" exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c \
  "SELECT concurrency FROM accounts WHERE id=${ACCOUNT_ID} AND deleted_at IS NULL LIMIT 1;" | tr -d '[:space:]')"

if [[ -z "$PLATFORM" || -z "$NAME" ]]; then
  echo "{\"verdict\":\"setup_error\",\"error\":\"account ${ACCOUNT_ID} not found\"}" >&2
  exit 2
fi

export NAME PLATFORM CONC
python3 - <<'PY'
import json, os
print(json.dumps({
    "phase": "target",
    "account_id": int(os.environ["ACCOUNT_ID"]),
    "name": os.environ["NAME"],
    "platform": os.environ["PLATFORM"],
    "concurrency": int(os.environ.get("CONC") or 0),
    "model": os.environ["MODEL"],
    "cases_path": os.environ["REQUEST_POOL"],
    "prompt_tokens_target": int(os.environ["PROMPT_TOKENS_TARGET"]) if os.environ.get("PROMPT_TOKENS_TARGET") else None,
    "max_tokens": int(os.environ["MAX_TOKENS"]) if os.environ.get("MAX_TOKENS") else None,
    "base_url": os.environ["BASE_URL"],
    "app_container": os.environ["APP_CONTAINER"],
}, ensure_ascii=False))
PY

cleanup() {
  local scope="${TK_PROBE_SCOPE:-}"
  if [[ -z "$scope" && -n "${PLATFORM:-}" ]] && declare -F tk_probe_scope_from_platform >/dev/null 2>&1; then
    scope="$(tk_probe_scope_from_platform "$PLATFORM" 2>/dev/null || true)"
  fi
  if [[ -n "$scope" ]]; then
    tk_probe_clear_bindings "$scope" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

tk_probe_prepare_platform_reuse_probe "$PLATFORM" "$ACCOUNT_ID" || {
  echo '{"verdict":"setup_error","error":"failed to bind probe group to account"}' >&2
  exit 2
}
PROBE_KEY="${TK_PROBE_KEY:?}"
PROBE_GROUP_ID="${TK_PROBE_GROUP_ID:?}"
PROBE_KEY_ID="${TK_PROBE_KEY_ID:?}"
PROBE_SCOPE="${TK_PROBE_SCOPE:?}"
echo "{\"phase\":\"pinned\",\"account_id\":${ACCOUNT_ID},\"probe_group_id\":${PROBE_GROUP_ID},\"probe_key_id\":${PROBE_KEY_ID},\"probe_scope\":\"${PROBE_SCOPE}\"}"
# Brief wait for scheduler_outbox group_changed to be applied.
sleep 3

export PROBE_KEY
REQUEST_LOG="${REQUEST_LOG:-/tmp/tk-single-acct-ladder-${ACCOUNT_ID}.jsonl}"
: > "$REQUEST_LOG"
export REQUEST_LOG
LADDER_METRICS_PY=""
for candidate in \
  "$SCRIPT_DIR/tpm_burst_recipe1.py" \
  /tmp/tpm_burst_recipe1.py; do
  if [[ -f "$candidate" ]]; then
    LADDER_METRICS_PY="$candidate"
    break
  fi
done
if [[ -z "$LADDER_METRICS_PY" ]]; then
  echo '{"verdict":"setup_error","error":"tpm_burst_recipe1.py not found; pass --with ops/observability/tpm_burst_recipe1.py"}' >&2
  exit 2
fi
export LADDER_METRICS_PY
# Summaries stay on stdout. Per-request rows go to REQUEST_LOG because SSM inline output is capped.
echo "{\"phase\":\"request_log_open\",\"path\":\"${REQUEST_LOG}\"}"
python3 - <<'PY'
from __future__ import annotations

import importlib.util
import json
import os
import statistics
import sys
import threading
import time
import urllib.error
import urllib.request
from collections import Counter
from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait

api_key = os.environ["PROBE_KEY"]
model = os.environ["MODEL"]
cases_path = os.environ["REQUEST_POOL"]
prompt_tokens_target = os.environ.get("PROMPT_TOKENS_TARGET", "").strip()
max_tokens_target = os.environ.get("MAX_TOKENS", "").strip()
steps = [int(x) for x in os.environ["STEPS"].split(",") if x.strip()]
hold_s = float(os.environ["HOLD_S"])
cal_n = int(os.environ["CALIBRATE_N"])
timeout_s = float(os.environ["TIMEOUT_S"])
base_url = os.environ["BASE_URL"].rstrip("/")
account_id = int(os.environ["ACCOUNT_ID"])
request_log_path = os.environ["REQUEST_LOG"]
metrics_py = os.environ["LADDER_METRICS_PY"]

_spec = importlib.util.spec_from_file_location("tpm_burst_recipe1", metrics_py)
if _spec is None or _spec.loader is None:
    raise SystemExit("cannot load tpm_burst_recipe1")
_metrics = importlib.util.module_from_spec(_spec)
sys.modules[_spec.name] = _metrics
_spec.loader.exec_module(_metrics)

if prompt_tokens_target:
    cases = [
        _metrics.synthetic_context_case(
            prompt_tokens_target=int(prompt_tokens_target),
            max_tokens=int(max_tokens_target or "1000"),
        )
    ]
    print(
        json.dumps(
            {
                "phase": "cases_loaded",
                "source": "synthetic_context",
                "prompt_tokens_target": int(prompt_tokens_target),
                "max_tokens": int(max_tokens_target or "1000"),
                "replayable_n": 1,
            },
            ensure_ascii=False,
        ),
        flush=True,
    )
else:
    try:
        cases = _metrics.load_chat_case_pool(cases_path)
    except (OSError, json.JSONDecodeError) as exc:
        print(json.dumps({"phase": "cases_unreadable", "path": cases_path, "error": type(exc).__name__}), flush=True)
        raise SystemExit(2)
    if not cases:
        print(json.dumps({"phase": "cases_empty", "path": cases_path}), flush=True)
        raise SystemExit(2)
    print(
        json.dumps(
            {"phase": "cases_loaded", "path": cases_path, **_metrics.summarize_case_pool(cases)},
            ensure_ascii=False,
        ),
        flush=True,
    )


def one(step_name: str = "ladder", inflight: int = 1) -> dict:
    global seq_n
    with seq_lock:
        seq_n += 1
        seq = seq_n
        case = cases[(seq - 1) % len(cases)]
    fields = _metrics.case_replay_fields(case)
    started_unix = time.time()
    t0 = time.perf_counter()
    body = json.dumps(_metrics.prepare_replay_body(case["body"], model=model)).encode()
    req = urllib.request.Request(
        base_url + "/v1/chat/completions",
        data=body,
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
            "Accept": "text/event-stream",
            "User-Agent": f"tokenkey-single-acct-ladder/{account_id}",
        },
        method="POST",
    )
    events: list[tuple[float, dict]] = []
    done = False
    transport_error = ""
    status = 0
    hdrs: dict[str, str] = {}
    try:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(req, timeout=timeout_s) as resp:
            status = resp.status
            hdrs = {k.lower(): v for k, v in resp.headers.items()}
            for raw_line in resp:
                now = time.perf_counter()
                if now - t0 > timeout_s:
                    transport_error = "request wall time exceeded timeout"
                    break
                line = raw_line.decode("utf-8", errors="replace").strip()
                if not line.startswith("data:"):
                    continue
                chunk = line[5:].strip()
                if chunk == "[DONE]":
                    done = True
                    break
                try:
                    obj = json.loads(chunk)
                except json.JSONDecodeError:
                    transport_error = "non-json sse"
                    break
                if isinstance(obj, dict):
                    events.append((now, obj))
    except urllib.error.HTTPError as e:
        status = e.code
        hdrs = {k.lower(): v for k, v in (e.headers.items() if e.headers else [])}
        raw = e.read() if e.fp else b""
        transport_error = raw.decode("utf-8", errors="replace")[:300]
        if api_key and api_key in transport_error:
            transport_error = transport_error.replace(api_key, "<redacted>")
    except Exception as e:  # noqa: BLE001 — surface transport failures in the request row
        transport_error = str(e)[:300]
        if api_key and api_key in transport_error:
            transport_error = transport_error.replace(api_key, "<redacted>")
    sample = _metrics.sample_from_stream_events(
        events,
        t0=t0,
        status=status,
        done=done,
        duration_s=time.perf_counter() - t0,
        transport_error=transport_error,
    )
    record = _metrics.request_record_from_sample(
        sample,
        account_id=account_id,
        model=model,
        step=step_name,
        inflight=inflight,
        seq=seq,
        started_unix=started_unix,
        request_id=hdrs.get("x-request-id"),
        case_sha256=fields.get("case_sha256"),
        reasoning_effort=fields.get("reasoning_effort"),
        output_limit=fields.get("output_limit"),
        output_limit_field=fields.get("output_limit_field"),
        redacted_marker=fields.get("redacted_marker"),
    )
    line = json.dumps(record, ensure_ascii=False)
    with log_lock:
        request_log.write(line + "\n")
        request_log.flush()
    return record


seq_n = 0
seq_lock = threading.Lock()
log_lock = threading.Lock()
request_log = open(request_log_path, "a", encoding="utf-8")


print(json.dumps({"phase": "calibrate_start", "n": cal_n}), flush=True)
cal = []
for i in range(cal_n):
    r = one("calibrate", 1)
    cal.append(r)
    print(json.dumps({"phase": "calibrate_sample", "i": i + 1, **{k: v for k, v in r.items() if k != "error"} , "error": (r.get("error") or "")[:120]}), flush=True)

ok_cal = [r for r in cal if r["ok"]]
cal_summary = {
    "phase": "calibrate_summary",
    "ok_n": len(ok_cal),
    "fail_n": len(cal) - len(ok_cal),
    "prompt_tokens_mean": round(statistics.mean(r["prompt_tokens"] for r in ok_cal), 1) if ok_cal else None,
    "completion_tokens_mean": round(statistics.mean(r["completion_tokens"] for r in ok_cal), 1) if ok_cal else None,
    "total_tokens_mean": round(statistics.mean(r["total_tokens"] for r in ok_cal), 1) if ok_cal else None,
    "latency_s_mean": round(statistics.mean(r["latency_s"] for r in ok_cal), 3) if ok_cal else None,
    "content_emitted_n": sum(1 for r in cal if r.get("content_emitted")),
    "fail_status_hist": dict(Counter(str(r["status_code"]) for r in cal if not r["ok"])),
}
print(json.dumps(cal_summary), flush=True)
if len(ok_cal) == 0:
    print(json.dumps({"phase": "aborted", "reason": "calibrate_all_failed"}), flush=True)
    raise SystemExit(3)

ladder = []
for inflight in steps:
    print(json.dumps({"phase": "step_start", "inflight": inflight, "hold_s": hold_s}), flush=True)
    t0 = time.time()
    deadline = t0 + hold_s
    results: list[dict] = []
    workers = max(1, inflight)
    with ThreadPoolExecutor(max_workers=workers) as pool:
        pending: set = set()
        while True:
            now = time.time()
            if now < deadline and len(pending) < workers:
                pending.add(pool.submit(one, "ladder", inflight))
                continue
            if not pending:
                break
            timeout = max(0.01, deadline - now) if now < deadline else None
            done, pending = wait(pending, timeout=timeout, return_when=FIRST_COMPLETED)
            for fut in done:
                results.append(fut.result())
    wall = time.time() - t0
    n = len(results)
    ok = [r for r in results if r["ok"]]
    tok_sum = sum(r["total_tokens"] for r in ok)
    # Count InflightBatchsize in error bodies
    inflight_hits = sum(
        1
        for r in results
        if r.get("error") and "InflightBatchsize" in str(r["error"])
    )
    step = {
        "phase": "step_summary",
        "inflight": inflight,
        "hold_s": hold_s,
        "n": n,
        "wall_s": round(wall, 2),
        "hold_filled": wall + 1e-6 >= hold_s,
        "success_n": len(ok),
        "fail_n": n - len(ok),
        "success_rate": round(len(ok) / n, 4) if n else 0,
        "content_emitted_n": sum(1 for r in results if r.get("content_emitted")),
        "content_rate": round(sum(1 for r in results if r.get("content_emitted")) / n, 4) if n else 0,
        "approx_rpm": round(len(ok) / wall * 60, 2) if wall > 0 else 0,
        "approx_tpm": round(tok_sum / wall * 60, 2) if wall > 0 else 0,
        "tok_sum_success": tok_sum,
        "latency_s_p50": round(statistics.median(r["latency_s"] for r in ok), 3) if ok else None,
        "latency_s_p90": round(sorted(r["latency_s"] for r in ok)[max(0, int(len(ok) * 0.9) - 1)], 3) if ok else None,
        "fail_status_hist": dict(Counter(str(r["status_code"]) for r in results if not r["ok"])),
        "inflight_batchsize_err_n": inflight_hits,
        "error_samples": [
            {"status": r["status_code"], "error": (r.get("error") or "")[:160]}
            for r in results
            if not r["ok"]
        ][:5],
    }
    print(json.dumps(step), flush=True)
    ladder.append(step)

print(
    json.dumps(
        {
            "phase": "ladder_complete",
            "account_id": account_id,
            "model": model,
            "calibrate": cal_summary,
            "steps": ladder,
            "request_log": request_log_path,
            "request_rows": seq_n,
        }
    ),
    flush=True,
)
request_log.close()
PY

# Attribution check: usage_logs for this account in the last 45 minutes
"${DOCKER_BIN[@]}" exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c "
SELECT row_to_json(t) FROM (
  SELECT
    count(*) AS n,
    count(*) FILTER (WHERE account_id = ${ACCOUNT_ID}) AS n_target,
    count(DISTINCT account_id) AS distinct_accounts,
    coalesce(sum(input_tokens + output_tokens) FILTER (WHERE account_id = ${ACCOUNT_ID}), 0) AS tok_target,
    min(created_at) AS first_ts,
    max(created_at) AS last_ts
  FROM usage_logs
  WHERE created_at >= now() - interval '45 minutes'
    AND user_agent LIKE 'tokenkey-single-acct-ladder/%'
) t;
" | awk '{print "{\"phase\":\"usage_attribution\",\"row\":"$0"}"}'
