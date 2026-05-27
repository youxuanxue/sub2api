#!/usr/bin/env bash
# Multi-request HTTP mitm capture for beta consistency check (Haiku / Sonnet / Opus).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HTTP_INVOKE="$SCRIPT_DIR/http_capture_invoke.sh"
PY="$SCRIPT_DIR/capture_cc_fingerprint.py"
MITM_ADDON="$SCRIPT_DIR/mitm_cc_http_headers.py"
OUT_DIR="${TOKENKEY_CC_CAPTURE_OUT_DIR:-$REPO_ROOT/.tls_list}"
MITM_PORT="${TOKENKEY_CC_CAPTURE_MITM_PORT:-11803}"
GOST_PORT="${CC0_GOST_HTTP_PORT:-11800}"

# Repeat counts per family (total requests = sum).
HAIKU_N="${TOKENKEY_CC_CAPTURE_HAIKU_N:-3}"
SONNET_N="${TOKENKEY_CC_CAPTURE_SONNET_N:-3}"
OPUS_N="${TOKENKEY_CC_CAPTURE_OPUS_N:-2}"

HAIKU_MODEL="${TOKENKEY_CC_CAPTURE_MODEL:-claude-haiku-4-5-20251001}"
SONNET_MODEL="${TOKENKEY_CC_CAPTURE_SONNET_MODEL:-claude-sonnet-4-20250514}"
OPUS_MODEL="${TOKENKEY_CC_CAPTURE_OPUS_MODEL:-claude-opus-4-5-20251101}"

[[ -f "${HOME}/.config/cc0/env" ]] && # shellcheck disable=SC1091
  source "${HOME}/.config/cc0/env"

require_cmd() { command -v "$1" >/dev/null || { echo "error: missing $1" >&2; exit 1; }; }
require_cmd mitmdump
require_cmd python3
require_cmd jq
[[ -x "$HTTP_INVOKE" ]] || { echo "error: missing $HTTP_INVOKE (need #427 http_capture_invoke.sh)" >&2; exit 1; }

_cc0_port_open() {
  python3 - "$1" "$2" <<'PY'
import socket, sys
s = socket.socket(); s.settimeout(2)
try: s.connect((sys.argv[1], int(sys.argv[2])))
except OSError: raise SystemExit(1)
finally: s.close()
PY
}

host="${CC0_GOST_HTTP_HOST:-127.0.0.1}"
port="${CC0_GOST_HTTP_PORT:-11800}"
if ! _cc0_port_open "$host" "$port"; then
  echo "error: gost not listening on http://${host}:${port}" >&2
  exit 1
fi

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$OUT_DIR"
work="$(mktemp -d "${TMPDIR:-/tmp}/tk-http-multi.XXXXXX")"
http_log="$OUT_DIR/${stamp}-http-multi.log"
: >"$http_log"

pkill -f "mitmdump.*${MITM_PORT}" 2>/dev/null || true  # preflight-allow: swallow
sleep 1
CC_CAPTURE_HTTP_LOG="$http_log" \
  mitmdump --mode "upstream:http://127.0.0.1:${GOST_PORT}" \
  -s "$MITM_ADDON" --listen-port "$MITM_PORT" \
  >"$work/mitm.out" 2>"$work/mitm.err" &
mitm_pid=$!
sleep 2

run_n() {
  local n="$1" model="$2" label="$3"
  local i
  for ((i = 1; i <= n; i++)); do
    echo "request ${label} ${i}/${n} model=${model}"
    "$HTTP_INVOKE" --mitm-port "$MITM_PORT" --model "$model" --work-dir "$work" || true  # preflight-allow: swallow
    sleep 1
  done
}

run_n "$HAIKU_N" "$HAIKU_MODEL" haiku
run_n "$SONNET_N" "$SONNET_MODEL" sonnet
run_n "$OPUS_N" "$OPUS_MODEL" opus

sleep 2
kill "$mitm_pid" 2>/dev/null || true  # preflight-allow: swallow
wait "$mitm_pid" 2>/dev/null || true  # preflight-allow: swallow

lines="$(wc -l <"$http_log" | tr -d ' ')"
if [[ "${lines:-0}" == "0" ]]; then
  echo "error: no HTTP records captured" >&2
  exit 1
fi

echo "http_log=$http_log (${lines} records)"
python3 - "$http_log" <<'PY'
import json, sys
from collections import defaultdict
path = sys.argv[1]
by_variant: dict[str, list[str]] = defaultdict(list)
for line in open(path, encoding="utf-8"):
    line = line.strip()
    if not line:
        continue
    if line.startswith("CC_CAPTURE "):
        line = line[len("CC_CAPTURE ") :]
    rec = json.loads(line)
    model = (rec.get("model") or "").lower()
    beta = rec.get("anthropic_beta") or ""
    if "haiku" in model:
        v = "haiku"
    elif "opus" in model:
        v = "opus"
    elif "sonnet" in model:
        v = "sonnet"
    else:
        v = model or "unknown"
    by_variant[v].append(beta)
print("=== HTTP capture consistency ===")
for v, betas in sorted(by_variant.items()):
    uniq = list(dict.fromkeys(betas))
    print(f"{v}: {len(betas)} requests, {len(uniq)} unique beta header(s)")
    for i, b in enumerate(betas, 1):
        print(f"  [{i}] {b}")
    if len(uniq) == 1:
        print(f"  OK all {v} requests identical")
    else:
        print(f"  WARN {v} beta headers differ across requests")
PY

# Pick last TLS bundle or run quick TLS - use latest tls-observed if exists
tls_json="$(ls -t "$OUT_DIR"/*-cc-capture.tls-observed.json 2>/dev/null | head -1 || true)"
if [[ -z "$tls_json" ]]; then
  echo "note: no tls-observed.json; run capture-cc-fingerprint.sh capture for TLS bundle" >&2
  exit 0
fi

cc_ver="$(jq -r '.cc_version // empty' "$(ls -t "$OUT_DIR"/*-cc-capture.bundle.json 2>/dev/null | head -1)" 2>/dev/null || true)"
[[ -z "$cc_ver" ]] && cc_ver="$("$HOME/.local/bin/claude" --version 2>/dev/null | awk '{print $1}' || true)"

bundle="$OUT_DIR/${stamp}-cc-capture.bundle.json"
python3 "$PY" bundle-from-artifacts \
  --tls-json "$tls_json" \
  --http-log "$http_log" \
  --cc-version "${cc_ver:-unknown}" \
  --out "$bundle" \
  --collector "https://tls.sub2api.org:8090"

echo "bundle=$bundle"
python3 "$PY" diff --bundle "$bundle"
python3 "$PY" check --bundle "$bundle"
rm -rf "$work"
