#!/usr/bin/env bash
# Pack plaintext entry host-config and leave a one-shot private-IP HTTP listener
# so the site data machine can curl the tar (entry has no archive upload CLI).
#
#   TK_HOST_CONFIG_SERVE=1 bash ops/observability/run-probe.sh \
#     --target prod|cn-prod --node entry \
#     --script ops/archive/probe-entry-host-config-pack-serve.sh \
#     --env TK_HOST_CONFIG_SITE=prod|cn-prod
#
# Exits immediately after binding; listener serves one GET then deletes the tar.
set -euo pipefail

if [ "${TK_HOST_CONFIG_SERVE:-}" != "1" ]; then
  echo '{"ok":false,"reason":"refused: set TK_HOST_CONFIG_SERVE=1"}'
  exit 64
fi

SITE="${TK_HOST_CONFIG_SITE:-prod}"
case "$SITE" in
  prod) LABEL="${TK_HOST_CONFIG_LABEL:-prod-entry}" ;;
  cn-prod) LABEL="${TK_HOST_CONFIG_LABEL:-cn-prod-entry}" ;;
  *) echo "{\"ok\":false,\"reason\":\"unsupported site $SITE\"}"; exit 64 ;;
esac

ROOT="${TK_ENTRY_HOST_ROOT:-/var/lib/tokenkey}"
PACK="/tmp/pack_entry_host_config.py"
[ -f "$PACK" ] || PACK="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pack_entry_host_config.py"
[ -f "$PACK" ] || { echo '{"ok":false,"reason":"pack_entry_host_config.py missing"}'; exit 2; }

PRIV_IP="$(ip -4 -o addr show scope global | awk '{print $4}' | head -1 | cut -d/ -f1)"
[ -n "$PRIV_IP" ] || { echo '{"ok":false,"reason":"no private ipv4"}'; exit 2; }
PORT="${TK_HOST_CONFIG_PORT:-8765}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
PENDING="$ROOT/host-config-pending"
OUT="$PENDING/${LABEL}-${STAMP}.tar.gz"
mkdir -p "$PENDING"
chmod 700 "$PENDING"

export TK_ENTRY_HOST_ROOT="$ROOT"
python3 "$PACK" pack --site "$SITE" --out "$OUT" >"$PENDING/last-pack.json"
SHA="$(python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$OUT")"
BYTES="$(wc -c <"$OUT" | tr -d ' ')"

nohup python3 - "$PRIV_IP" "$PORT" "$OUT" >"$PENDING/serve.log" 2>&1 <<'PY' &
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

bind, port_s, path_s = sys.argv[1], sys.argv[2], sys.argv[3]
port = int(port_s)
blob = Path(path_s)
payload = blob.read_bytes()

class Once(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/gzip")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)
        raise SystemExit(0)
    def log_message(self, fmt, *args):
        return

httpd = HTTPServer((bind, port), Once)
httpd.timeout = 180
httpd.handle_request()
try:
    blob.unlink()
except OSError:
    pass
PY
disown || true
sleep 0.4

python3 - <<PY
import json
print(json.dumps({
  "ok": True,
  "site": "${SITE}",
  "label": "${LABEL}",
  "url": "http://${PRIV_IP}:${PORT}/${LABEL}.tar.gz",
  "private_ip": "${PRIV_IP}",
  "port": int("${PORT}"),
  "sha256": "${SHA}",
  "bytes": int("${BYTES}"),
  "pending_path": "${OUT}",
}, ensure_ascii=False, indent=2))
PY
