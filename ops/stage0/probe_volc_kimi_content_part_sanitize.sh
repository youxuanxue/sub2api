#!/usr/bin/env bash
# Direct-Volc evidence: raw vs sanitized content parts for kimi-* Agent Plan.
# Proves sanitize→200 for drop/rewrite candidates and keeps KVV negatives at 400.
# Never prints api_key.
#
# Skill entry: tokenkey-account-model-probe (KVV / supplier-shape evidence).
#
#   bash ops/observability/run-probe.sh --target cn-prod --node app-b --via ssm \
#     --script ops/stage0/probe_volc_kimi_content_part_sanitize.sh \
#     --env VOLC_ACCOUNT_ID=9 --env MODEL=kimi-k3
#
# Env:
#   VOLC_ACCOUNT_ID   required (Volc Agent Plan / vap account)
#   MODEL             default kimi-k3
#   REQUEST_TIMEOUT_SECONDS default 90
set -euo pipefail

VOLC_ACCOUNT_ID="${VOLC_ACCOUNT_ID:-}"
MODEL="${MODEL:-kimi-k3}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-90}"

if [ -z "$VOLC_ACCOUNT_ID" ]; then
  echo '{"verdict":"setup_error","error":"VOLC_ACCOUNT_ID required"}'
  exit 0
fi

for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || {
  echo '{"verdict":"setup_error","error":"resolve-app-container.sh missing"}'
  exit 0
}
export TK_PG
TK_PG="$(tk_require_postgres_container "${POSTGRES_CONTAINER:-auto}")" || {
  echo '{"verdict":"setup_error","error":"postgres container missing"}'
  exit 0
}

python3 - "$VOLC_ACCOUNT_ID" "$MODEL" "$REQUEST_TIMEOUT_SECONDS" <<'PY'
import base64
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request
from urllib.parse import urljoin

account_id_raw, model, timeout_raw = sys.argv[1:4]
account_id = int(account_id_raw)
timeout = int(timeout_raw)


def fail(msg: str) -> None:
    print(json.dumps({"verdict": "setup_error", "error": msg}, ensure_ascii=False))
    raise SystemExit(0)


def psql(sql: str) -> str:
    cmd = [
        "sudo", "docker", "exec", "-i", os.environ["TK_PG"], "psql",
        "-U", "tokenkey", "-d", "tokenkey", "-X", "-q", "-A", "-t",
        "-v", "ON_ERROR_STOP=1", "-c", sql,
    ]
    try:
        return subprocess.run(cmd, check=True, text=True, capture_output=True).stdout.strip()
    except subprocess.CalledProcessError as exc:
        err = re.sub(
            r"(password|token|secret|key)[^\s]*",
            r"\1=<redacted>",
            (exc.stderr or str(exc))[:500],
            flags=re.I,
        )
        fail(f"psql failed: {err}")


def load_account(aid: int) -> dict:
    raw = psql(f"""
SELECT json_build_object(
  'id', a.id,
  'name', COALESCE(a.name, ''),
  'platform', COALESCE(a.platform, ''),
  'status', COALESCE(a.status, ''),
  'schedulable', COALESCE(a.schedulable, false),
  'api_key', COALESCE(a.credentials->>'api_key', ''),
  'base_url', COALESCE(a.credentials->>'base_url', ''),
  'model_mapping', COALESCE(a.credentials->'model_mapping', '{{}}'::jsonb)
)::text
FROM accounts a
WHERE a.id={aid} AND a.deleted_at IS NULL;
""")
    if not raw:
        fail(f"account {aid} not found")
    return json.loads(raw)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


opener = urllib.request.build_opener(NoRedirect())


def chat(api_key: str, base_url: str, body: dict):
    url = urljoin(base_url.rstrip("/") + "/", "chat/completions")
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=data,
        method="POST",
        headers={
            "Authorization": f"Bearer {api_key}",
            "Accept": "application/json",
            "Content-Type": "application/json",
        },
    )
    try:
        with opener.open(req, timeout=timeout) as resp:
            return resp.status, resp.read(2 << 20)
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read(16384)
    except Exception as exc:  # noqa: BLE001
        return None, str(exc).encode("utf-8")


def summarize(status, raw: bytes) -> dict:
    out = {"http": status}
    try:
        obj = json.loads(raw.decode("utf-8", errors="replace"))
    except Exception:
        out["raw_preview"] = raw.decode("utf-8", errors="replace")[:240]
        return out
    if not isinstance(obj, dict):
        out["raw_preview"] = str(obj)[:240]
        return out
    if status != 200:
        err = obj.get("error") if isinstance(obj.get("error"), dict) else {}
        msg = err.get("message") or obj.get("message") or ""
        out["error"] = {
            "type": err.get("type") or obj.get("type"),
            "message": str(msg)[:220],
            "param": err.get("param"),
            "code": err.get("code"),
        }
        return out
    usage = obj.get("usage") if isinstance(obj.get("usage"), dict) else {}
    choices = obj.get("choices") if isinstance(obj.get("choices"), list) else []
    finish = None
    if choices and isinstance(choices[0], dict):
        finish = choices[0].get("finish_reason")
    out["usage"] = {
        "prompt_tokens": usage.get("prompt_tokens"),
        "completion_tokens": usage.get("completion_tokens"),
    }
    out["finish_reason"] = finish
    out["has_choices"] = bool(choices)
    return out


def upstream_model(acct: dict, public: str) -> str:
    mapping = acct.get("model_mapping") or {}
    if isinstance(mapping, dict) and public in mapping and mapping[public]:
        return str(mapping[public])
    return public


acct = load_account(account_id)
if not acct.get("api_key") or not acct.get("base_url"):
    fail("account missing api_key or base_url")
base_url = str(acct["base_url"]).rstrip("/")
if "volces.com" not in base_url and "volcengine" not in base_url:
    fail(f"account base_url does not look like Volc Agent Plan: {base_url}")

up_model = upstream_model(acct, model)
video_bytes = b"\x00\x00\x00\x18ftypmp42" + (b"\x00" * 280)
video_data_url = "data:video/mp4;base64," + base64.b64encode(video_bytes).decode("ascii")

# Candidate shapes: raw (expect type-enum 400) vs sanitized (expect 200 where possible).
cases = []

# 1) refusal drop — history replay, no deliverable user text in the part
raw_refusal = {
    "model": up_model,
    "max_tokens": 16,
    "messages": [
        {"role": "user", "content": "Say OK."},
        {
            "role": "assistant",
            "content": [
                {"type": "refusal", "refusal": "blocked"},
                {"type": "text", "text": "OK"},
            ],
        },
        {"role": "user", "content": "Reply with the single word: pong"},
    ],
}
san_refusal = {
    "model": up_model,
    "max_tokens": 16,
    "messages": [
        {"role": "user", "content": "Say OK."},
        {"role": "assistant", "content": [{"type": "text", "text": "OK"}]},
        {"role": "user", "content": "Reply with the single word: pong"},
    ],
}
cases.append(("refusal_drop", raw_refusal, san_refusal, "kvv_ok_drop_non_deliverable"))

# 2) summary_text drop
raw_summary = {
    "model": up_model,
    "max_tokens": 16,
    "messages": [
        {"role": "user", "content": "Hi"},
        {
            "role": "assistant",
            "content": [
                {"type": "summary_text", "text": "prior summary"},
                {"type": "text", "text": "hello"},
            ],
        },
        {"role": "user", "content": "Reply with the single word: pong"},
    ],
}
san_summary = {
    "model": up_model,
    "max_tokens": 16,
    "messages": [
        {"role": "user", "content": "Hi"},
        {"role": "assistant", "content": [{"type": "text", "text": "hello"}]},
        {"role": "user", "content": "Reply with the single word: pong"},
    ],
}
cases.append(("summary_text_drop", raw_summary, san_summary, "kvv_ok_drop_non_deliverable"))

# 3) input_video rewrite — same URL payload, only type changes
raw_input_video = {
    "model": up_model,
    "max_tokens": 16,
    "messages": [
        {
            "role": "user",
            "content": [
                {
                    "type": "input_video",
                    "video_url": {"url": video_data_url},
                },
                {"type": "text", "text": "Reply with the single word: pong"},
            ],
        }
    ],
}
san_input_video = {
    "model": up_model,
    "max_tokens": 16,
    "messages": [
        {
            "role": "user",
            "content": [
                {
                    "type": "video_url",
                    "video_url": {"url": video_data_url},
                },
                {"type": "text", "text": "Reply with the single word: pong"},
            ],
        }
    ],
}
cases.append(("input_video_rewrite", raw_input_video, san_input_video, "kvv_ok_rewrite_alias_type"))

# KVV negatives — must stay 400; we do not sanitize these away.
kvv_negatives = []

orphan = {
    "model": up_model,
    "max_tokens": 16,
    "messages": [
        {"role": "user", "content": "hi"},
        {
            "role": "tool",
            "tool_call_id": "call_missing_orphan",
            "content": "result",
        },
    ],
}
kvv_negatives.append(("orphan_tool_call_id", orphan, "must_400_kvv"))

blank_name = {
    "model": up_model,
    "max_tokens": 16,
    "tools": [
        {
            "type": "function",
            "function": {"name": "", "parameters": {"type": "object", "properties": {}}},
        }
    ],
    "messages": [{"role": "user", "content": "hi"}],
}
kvv_negatives.append(("blank_function_name", blank_name, "must_400_kvv"))

# temperature 1.1 is gateway-local KVV (Volc may silently accept); do not use
# as an upstream must-400 control. Orphan + blank name cover fail-closed.

results = []
for name, raw_body, san_body, posture in cases:
    raw_status, raw_bytes = chat(acct["api_key"], base_url, raw_body)
    san_status, san_bytes = chat(acct["api_key"], base_url, san_body)
    raw_sum = summarize(raw_status, raw_bytes)
    san_sum = summarize(san_status, san_bytes)
    raw_ok_fail = raw_status is not None and raw_status >= 400
    san_ok = san_status == 200
    # For input_video, sanitized may still fail on media validity; enum must change.
    enum_cleared = False
    if name == "input_video_rewrite":
        raw_msg = str((raw_sum.get("error") or {}).get("message") or "")
        san_msg = str((san_sum.get("error") or {}).get("message") or "")
        enum_cleared = ("input_video" in raw_msg) and ("input_video" not in san_msg)
    passed = bool(raw_ok_fail and (san_ok or enum_cleared))
    results.append(
        {
            "case": name,
            "posture": posture,
            "passed": passed,
            "raw": raw_sum,
            "sanitized": san_sum,
            "enum_cleared": enum_cleared if name == "input_video_rewrite" else None,
            "sanitize_to_200": san_ok,
        }
    )

neg_results = []
for name, body, posture in kvv_negatives:
    status, raw = chat(acct["api_key"], base_url, body)
    summary = summarize(status, raw)
    passed = status is not None and status >= 400
    neg_results.append(
        {
            "case": name,
            "posture": posture,
            "passed": passed,
            "result": summary,
        }
    )

all_pass = all(r["passed"] for r in results) and all(r["passed"] for r in neg_results)
# Stronger gate for drop cases: require sanitize→200
drop_200 = all(
    r["sanitize_to_200"]
    for r in results
    if r["case"] in ("refusal_drop", "summary_text_drop")
)
verdict = "ok" if all_pass and drop_200 else ("partial" if any(r["passed"] for r in results) else "failed")

print(
    json.dumps(
        {
            "verdict": verdict,
            "kind": "volc_kimi_content_part_sanitize",
            "account": {
                "id": acct["id"],
                "name": acct["name"],
                "platform": acct["platform"],
                "base_url": base_url,
            },
            "public_model": model,
            "upstream_model": up_model,
            "candidates": results,
            "kvv_negatives": neg_results,
            "notes": [
                "Drop cases require raw>=400 and sanitized HTTP 200.",
                "input_video may clear type-enum without full 200 if media bytes are rejected; enum_cleared is still evidence for rewrite.",
                "KVV negatives must remain >=400 (orphan tool / blank function.name).",
                "Illegal temperature 1.1 is enforced locally by the gateway (KVV), not by Volc upstream.",
            ],
        },
        ensure_ascii=False,
    )
)
PY
