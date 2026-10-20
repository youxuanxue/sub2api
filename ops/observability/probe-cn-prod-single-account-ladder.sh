#!/usr/bin/env bash
# Pin one cn-prod NewAPI account via reserved probe group and ladder concurrency
# at ~5k input / 1k output. Emits metrics only (never prints the probe API key).
#
# Env:
#   ACCOUNT_ID   required numeric account id
#   MODEL        default kimi-k3
#   PROMPT_TOKENS_TARGET  default 5000
#   MAX_TOKENS   default 1000
#   STEPS        default "1,2,4,8,12,16"
#   REQS_PER_STEP default 12
#   CALIBRATE_N  default 2
#   BASE_URL     default active-color loopback (18081 blue / 18082 green)
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
MODEL="${MODEL:-kimi-k3}"
PROMPT_TOKENS_TARGET="${PROMPT_TOKENS_TARGET:-5000}"
MAX_TOKENS="${MAX_TOKENS:-1000}"
STEPS="${STEPS:-1,2,4,8,12,16}"
REQS_PER_STEP="${REQS_PER_STEP:-12}"
CALIBRATE_N="${CALIBRATE_N:-2}"
TIMEOUT_S="${TIMEOUT_S:-180}"
export ACCOUNT_ID MODEL PROMPT_TOKENS_TARGET MAX_TOKENS STEPS REQS_PER_STEP CALIBRATE_N TIMEOUT_S

if [[ ! "$ACCOUNT_ID" =~ ^[0-9]+$ ]]; then
  echo '{"verdict":"setup_error","error":"ACCOUNT_ID must be numeric"}' >&2
  exit 2
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

# Blue/green loopback ports from domestic compose.local.yml convention.
case "$APP_CONTAINER" in
  *-blue) APP_LOOP_URL="http://127.0.0.1:18081" ;;
  *-green) APP_LOOP_URL="http://127.0.0.1:18082" ;;
  *) APP_LOOP_URL="http://127.0.0.1:18080" ;;
esac
BASE_URL="${BASE_URL:-$APP_LOOP_URL}"
export BASE_URL

PLATFORM="$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c \
  "SELECT platform FROM accounts WHERE id=${ACCOUNT_ID} AND deleted_at IS NULL LIMIT 1;" | tr -d '[:space:]')"
NAME="$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c \
  "SELECT name FROM accounts WHERE id=${ACCOUNT_ID} AND deleted_at IS NULL LIMIT 1;" | tr -d '\n')"
CONC="$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c \
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
    "prompt_tokens_target": int(os.environ["PROMPT_TOKENS_TARGET"]),
    "max_tokens": int(os.environ["MAX_TOKENS"]),
    "base_url": os.environ["BASE_URL"],
    "app_container": os.environ["APP_CONTAINER"],
}, ensure_ascii=False))
PY

cleanup() {
  tk_probe_clear_bindings newapi >/dev/null 2>&1 || true
}
trap cleanup EXIT

tk_probe_prepare_platform_reuse_probe "$PLATFORM" "$ACCOUNT_ID" || {
  echo '{"verdict":"setup_error","error":"failed to bind probe group to account"}' >&2
  exit 2
}
PROBE_KEY="${TK_PROBE_KEY:?}"
PROBE_GROUP_ID="${TK_PROBE_GROUP_ID:?}"
PROBE_KEY_ID="${TK_PROBE_KEY_ID:?}"
echo "{\"phase\":\"pinned\",\"account_id\":${ACCOUNT_ID},\"probe_group_id\":${PROBE_GROUP_ID},\"probe_key_id\":${PROBE_KEY_ID}}"
# Brief wait for scheduler_outbox group_changed to be applied.
sleep 3

export PROBE_KEY
# Run on host Python against the active color loopback (never print PROBE_KEY).
python3 - <<'PY'
from __future__ import annotations

import json
import os
import statistics
import time
import urllib.error
import urllib.request
from collections import Counter
from concurrent.futures import ThreadPoolExecutor, as_completed

api_key = os.environ["PROBE_KEY"]
model = os.environ["MODEL"]
prompt_target = int(os.environ["PROMPT_TOKENS_TARGET"])
max_tokens = int(os.environ["MAX_TOKENS"])
steps = [int(x) for x in os.environ["STEPS"].split(",") if x.strip()]
reqs_per_step = int(os.environ["REQS_PER_STEP"])
cal_n = int(os.environ["CALIBRATE_N"])
timeout_s = float(os.environ["TIMEOUT_S"])
base_url = os.environ["BASE_URL"].rstrip("/")
account_id = int(os.environ["ACCOUNT_ID"])


def pad_prompt(target_tokens: int) -> str:
    unit = "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda "
    need_chars = max(64, int(target_tokens * 4))
    return (unit * (need_chars // len(unit) + 1))[:need_chars]


messages = [
    {
        "role": "user",
        "content": (
            f"Load-test single-account pin id={account_id}. "
            f"Reply with a dense English essay until the output limit. "
            f"Ignore filler except as bulk.\n\n"
            f"{pad_prompt(prompt_target)}"
        ),
    }
]


def one() -> dict:
    body = json.dumps(
        {
            "model": model,
            "messages": messages,
            "max_tokens": max_tokens,
            "stream": False,
            "temperature": 0.2,
        }
    ).encode()
    req = urllib.request.Request(
        base_url + "/v1/chat/completions",
        data=body,
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
            "User-Agent": f"tokenkey-single-acct-ladder/{account_id}",
        },
        method="POST",
    )
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=timeout_s) as resp:
            raw = resp.read()
            status = resp.status
            hdrs = {k.lower(): v for k, v in resp.headers.items()}
    except urllib.error.HTTPError as e:
        raw = e.read() if e.fp else b""
        status = e.code
        hdrs = {k.lower(): v for k, v in (e.headers.items() if e.headers else [])}
    except Exception as e:  # noqa: BLE001 — surface transport failures in metrics
        return {
            "ok": False,
            "status_code": 0,
            "error": type(e).__name__,
            "latency_s": round(time.time() - t0, 3),
            "prompt_tokens": 0,
            "completion_tokens": 0,
            "total_tokens": 0,
            "request_id": None,
        }
    latency = time.time() - t0
    prompt_tokens = completion_tokens = total_tokens = 0
    err = None
    try:
        data = json.loads(raw.decode("utf-8", errors="replace"))
        usage = data.get("usage") or {}
        prompt_tokens = int(usage.get("prompt_tokens") or 0)
        completion_tokens = int(usage.get("completion_tokens") or 0)
        total_tokens = int(usage.get("total_tokens") or (prompt_tokens + completion_tokens))
        if status >= 400:
            err = str(data.get("error") or data)[:240]
    except Exception as e:  # noqa: BLE001
        err = f"parse:{type(e).__name__}"
        data = None
    return {
        "ok": 200 <= status < 300 and total_tokens > 0,
        "status_code": status,
        "error": err,
        "latency_s": round(latency, 3),
        "prompt_tokens": prompt_tokens,
        "completion_tokens": completion_tokens,
        "total_tokens": total_tokens,
        "request_id": hdrs.get("x-request-id"),
    }


def summarize(label: str, inflight: int, results: list[dict]) -> dict:
    ok = [r for r in results if r["ok"]]
    wall = max((r["latency_s"] for r in results), default=0)  # replaced below
    return {"label": label, "inflight": inflight, "n": len(results), "ok_n": len(ok)}


print(json.dumps({"phase": "calibrate_start", "n": cal_n}), flush=True)
cal = []
for i in range(cal_n):
    r = one()
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
    "fail_status_hist": dict(Counter(str(r["status_code"]) for r in cal if not r["ok"])),
}
print(json.dumps(cal_summary), flush=True)
if len(ok_cal) == 0:
    print(json.dumps({"phase": "aborted", "reason": "calibrate_all_failed"}), flush=True)
    raise SystemExit(3)

ladder = []
for inflight in steps:
    n = reqs_per_step
    print(json.dumps({"phase": "step_start", "inflight": inflight, "n": n}), flush=True)
    t0 = time.time()
    results: list[dict] = []
    with ThreadPoolExecutor(max_workers=inflight) as pool:
        futs = [pool.submit(one) for _ in range(n)]
        for fut in as_completed(futs):
            results.append(fut.result())
    wall = time.time() - t0
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
        "n": n,
        "wall_s": round(wall, 2),
        "success_n": len(ok),
        "fail_n": n - len(ok),
        "success_rate": round(len(ok) / n, 4) if n else 0,
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
        }
    ),
    flush=True,
)
PY

# Attribution check: usage_logs for this account in the last 30 minutes
docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c "
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
