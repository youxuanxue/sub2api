#!/usr/bin/env bash
# Compare kimi-k3.1-pro request/response behavior between NVIDIA Build and
# Volc Agent Plan — direct upstream, bypassing TokenKey gateway. Output never
# includes api_key.
#
# Skill entry: tokenkey-account-model-probe (KVV / supplier-shape evidence).
# Deliver via run-probe; contract in ops/observability/probe-contracts.json.
#
#   bash ops/observability/run-probe.sh --target prod --node app-old --via ssm \
#     --script ops/stage0/probe_kimi31_nvidia_vs_volc.sh \
#     --env NVIDIA_ACCOUNT_ID=138 --env VOLC_ACCOUNT_ID=141
#
# Env:
#   NVIDIA_ACCOUNT_ID  default 138
#   VOLC_ACCOUNT_ID    default 141 (schedulable volcengine-agent-plan-6)
#   REQUEST_TIMEOUT_SECONDS default 90
set -euo pipefail

NVIDIA_ACCOUNT_ID="${NVIDIA_ACCOUNT_ID:-138}"
VOLC_ACCOUNT_ID="${VOLC_ACCOUNT_ID:-141}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-90}"

for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found" >&2; exit 1; }
TK_PG="$(tk_require_postgres_container "${POSTGRES_CONTAINER:-auto}")" || exit 1
export TK_PG

python3 - "$NVIDIA_ACCOUNT_ID" "$VOLC_ACCOUNT_ID" "$REQUEST_TIMEOUT_SECONDS" <<'PY'
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request
from urllib.parse import urljoin

nvidia_id, volc_id, timeout_raw = sys.argv[1:4]
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
        err = re.sub(r"(password|token|secret|key)[^\s]*", r"\1=<redacted>", (exc.stderr or str(exc))[:500], flags=re.I)
        fail(f"psql failed: {err}")


def load_account(account_id: int) -> dict:
    raw = psql(f"""
SELECT json_build_object(
  'id', a.id,
  'name', COALESCE(a.name, ''),
  'platform', COALESCE(a.platform, ''),
  'status', COALESCE(a.status, ''),
  'schedulable', COALESCE(a.schedulable, false),
  'channel_type', a.channel_type,
  'api_key', COALESCE(a.credentials->>'api_key', ''),
  'base_url', COALESCE(a.credentials->>'base_url', ''),
  'model_mapping', COALESCE(a.credentials->'model_mapping', '{{}}'::jsonb)
)::text
FROM accounts a
WHERE a.id={account_id} AND a.deleted_at IS NULL;
""")
    if not raw:
        fail(f"account {account_id} not found")
    return json.loads(raw)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


opener = urllib.request.build_opener(NoRedirect())


def chat(api_key: str, base_url: str, body: dict, path_suffix: str):
    # NVIDIA base is host-only → /v1/chat/completions.
    # Volc Agent Plan base already includes /api/plan/v3 → /chat/completions.
    url = urljoin(base_url.rstrip("/") + "/", path_suffix.lstrip("/"))
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
            raw = resp.read(2 << 20)
            return resp.status, raw
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read(16384)
    except Exception as exc:  # noqa: BLE001
        return None, str(exc).encode("utf-8")


def summarize_response(status, raw: bytes) -> dict:
    out = {"http": status}
    try:
        obj = json.loads(raw.decode("utf-8", errors="replace"))
    except Exception:
        out["raw_preview"] = raw.decode("utf-8", errors="replace")[:300]
        return out
    if not isinstance(obj, dict):
        out["raw_preview"] = str(obj)[:300]
        return out
    if status != 200:
        err = obj.get("error") if isinstance(obj.get("error"), dict) else {}
        out["error"] = {
            "message": (err.get("message") or obj.get("message") or obj.get("detail") or "")[:240],
            "type": err.get("type") or obj.get("type"),
            "code": err.get("code") or obj.get("code") or obj.get("status"),
            "param": err.get("param"),
        }
        if not out["error"]["message"] and "detail" in obj:
            out["error"]["message"] = str(obj.get("detail"))[:240]
        return out
    choice0 = (obj.get("choices") or [{}])[0] if isinstance(obj.get("choices"), list) else {}
    msg = choice0.get("message") if isinstance(choice0, dict) else {}
    if not isinstance(msg, dict):
        msg = {}
    usage = obj.get("usage") if isinstance(obj.get("usage"), dict) else {}
    out.update({
        "model": obj.get("model"),
        "object": obj.get("object"),
        "finish_reason": choice0.get("finish_reason"),
        "message_keys": sorted(msg.keys()),
        "content_preview": (msg.get("content") or "")[:120] if isinstance(msg.get("content"), str) else msg.get("content"),
        "has_reasoning_content": bool(msg.get("reasoning_content")),
        "has_reasoning": bool(msg.get("reasoning")),
        "tool_calls_count": len(msg.get("tool_calls") or []) if isinstance(msg.get("tool_calls"), list) else 0,
        "usage_keys": sorted(usage.keys()),
        "usage": {
            k: usage.get(k)
            for k in (
                "prompt_tokens", "completion_tokens", "total_tokens",
                "reasoning_tokens", "completion_tokens_details", "prompt_tokens_details",
            )
            if k in usage
        },
    })
    # nested reasoning token details if present
    ctd = usage.get("completion_tokens_details")
    if isinstance(ctd, dict) and "reasoning_tokens" in ctd:
        out["usage"]["completion_tokens_details.reasoning_tokens"] = ctd.get("reasoning_tokens")
    return out


nvidia = load_account(int(nvidia_id))
volc = load_account(int(volc_id))
for label, acct in (("nvidia", nvidia), ("volc", volc)):
    if not str(acct.get("api_key") or "").strip():
        fail(f"{label} account missing api_key")
    if not str(acct.get("base_url") or "").strip():
        fail(f"{label} account missing base_url")

suppliers = {
    "nvidia": {
        "account_id": nvidia["id"],
        "name": nvidia["name"],
        "base_url": str(nvidia["base_url"]).rstrip("/"),
        "api_key": nvidia["api_key"],
        "upstream_model": "moonshotai/kimi-k3.1-pro",
        "client_model": "kimi-k3.1-pro",
        "chat_path": "v1/chat/completions",
    },
    "volc": {
        "account_id": volc["id"],
        "name": volc["name"],
        "base_url": str(volc["base_url"]).rstrip("/"),
        "api_key": volc["api_key"],
        "upstream_model": "kimi-k3.1-pro",
        "client_model": "kimi-k3.1-pro",
        "chat_path": "chat/completions",
    },
}

# KVV-relevant request shapes (Moonshot public surface / Volc constraints).
# Bodies use each supplier's upstream model id.
cases = [
    ("baseline", {"messages": [{"role": "user", "content": "Reply with exactly OK."}], "max_tokens": 16, "stream": False}),
    ("temp_0.6_thinking_off", {"messages": [{"role": "user", "content": "Reply with exactly OK."}], "max_tokens": 16, "stream": False, "temperature": 0.6, "thinking": {"type": "disabled"}}),
    ("temp_1_thinking_on", {"messages": [{"role": "user", "content": "Reply with exactly OK."}], "max_tokens": 32, "stream": False, "temperature": 1, "thinking": {"type": "enabled"}}),
    ("temp_0.7_thinking_off", {"messages": [{"role": "user", "content": "Reply with exactly OK."}], "max_tokens": 16, "stream": False, "temperature": 0.7, "thinking": {"type": "disabled"}}),
    ("temp_0.7_thinking_on", {"messages": [{"role": "user", "content": "Reply with exactly OK."}], "max_tokens": 16, "stream": False, "temperature": 0.7, "thinking": {"type": "enabled"}}),
    ("top_p_1", {"messages": [{"role": "user", "content": "Reply with exactly OK."}], "max_tokens": 16, "stream": False, "top_p": 1}),
    ("n_2", {"messages": [{"role": "user", "content": "Reply with exactly OK."}], "max_tokens": 8, "stream": False, "n": 2}),
    ("presence_penalty_1", {"messages": [{"role": "user", "content": "Reply with exactly OK."}], "max_tokens": 8, "stream": False, "presence_penalty": 1}),
    ("reasoning_effort_high", {"messages": [{"role": "user", "content": "Reply with exactly OK."}], "max_tokens": 32, "stream": False, "reasoning_effort": "high"}),
    ("tools_required", {
        "messages": [{"role": "user", "content": "What is 2+2? Use the calculator tool."}],
        "max_tokens": 64,
        "stream": False,
        "tools": [{
            "type": "function",
            "function": {
                "name": "calculator",
                "description": "Add two numbers",
                "parameters": {
                    "type": "object",
                    "properties": {"a": {"type": "number"}, "b": {"type": "number"}},
                    "required": ["a", "b"],
                },
            },
        }],
        "tool_choice": "required",
    }),
]

results = {}
for case_name, base_body in cases:
    results[case_name] = {}
    for supplier_name, cfg in suppliers.items():
        body = dict(base_body)
        body["model"] = cfg["upstream_model"]
        status, raw = chat(cfg["api_key"], cfg["base_url"], body, cfg["chat_path"])
        results[case_name][supplier_name] = summarize_response(status, raw)

# Response-field union for baseline success comparison
baseline_cmp = {}
for supplier_name in suppliers:
    r = results["baseline"][supplier_name]
    baseline_cmp[supplier_name] = {
        k: r.get(k)
        for k in (
            "http", "model", "object", "finish_reason", "message_keys",
            "has_reasoning_content", "has_reasoning", "tool_calls_count",
            "usage_keys", "usage", "content_preview", "error",
        )
        if k in r
    }

# KVV compliance heuristic vs Moonshot table (not Volc pin):
# gateway rejects: temp 1.1/2, top_p!=0.95, presence/frequency !=0, n!=1
# Volc pin (supplier-specific): thinking on temp→1, off→0.6
kvv_notes = {
    "sampling_table": "KVV Moonshot table: temperature in [0,2] except 1.1/2.0; top_p must be 0.95 if set; presence/frequency must be 0 if set; n=1 only",
    "volc_pin": "Volc-only gateway pin: thinking-on legal temp→1; thinking-off legal temp→0.6 (direct upstream may still 400)",
    "scope": "Volc normalizers (content-part / temp pin) do NOT apply to NVIDIA accounts; KVV sampling rejects via isKimiModel still apply on gateway for kimi-*",
}

out = {
    "verdict": "ok",
    "probe": {
        "kind": "kimi31_nvidia_vs_volc_direct",
        "nvidia": {k: v for k, v in suppliers["nvidia"].items() if k != "api_key"},
        "volc": {k: v for k, v in suppliers["volc"].items() if k != "api_key"},
        "kvv_notes": kvv_notes,
    },
    "cases": results,
    "baseline_response_compare": baseline_cmp,
}
print(json.dumps(out, ensure_ascii=False))
PY
