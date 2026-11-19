#!/usr/bin/env bash
# Pack plaintext entry host-config and leave a one-shot private-IP HTTP listener
# so prod-data can curl the tar (entry has no cos-archive.env / aws CLI).
#
#   TK_HOST_CONFIG_SERVE=1 bash ops/observability/run-probe.sh \
#     --target prod --node entry \
#     --script ops/archive/probe-entry-host-config-pack-serve.sh
#
# Exits immediately after binding; listener serves one GET then deletes the tar.
set -euo pipefail

if [ "${TK_HOST_CONFIG_SERVE:-}" != "1" ]; then
  echo '{"ok":false,"reason":"refused: set TK_HOST_CONFIG_SERVE=1"}'
  exit 64
fi

ROOT="${TK_ENTRY_HOST_ROOT:-/var/lib/tokenkey}"
PACK="/tmp/pack_entry_host_config.py"
[ -f "$PACK" ] || PACK="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pack_entry_host_config.py"
[ -f "$PACK" ] || { echo '{"ok":false,"reason":"pack_entry_host_config.py missing"}'; exit 2; }

PRIV_IP="$(ip -4 -o addr show scope global | awk '{print $4}' | head -1 | cut -d/ -f1)"
[ -n "$PRIV_IP" ] || { echo '{"ok":false,"reason":"no private ipv4"}'; exit 2; }
# 8765 matches prior temp vault-pull firewall pattern on entry.
PORT="${TK_HOST_CONFIG_PORT:-8765}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="/var/lib/tokenkey/host-config-pending/prod-entry-${STAMP}.tar.gz"
mkdir -p "$(dirname "$OUT")"
chmod 700 "$(dirname "$OUT")"

export TK_ENTRY_HOST_ROOT="$ROOT"
python3 "$PACK" pack --site prod --out "$OUT" >/var/lib/tokenkey/host-config-pending/last-pack.json
SHA="$(python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$OUT")"
BYTES="$(wc -c <"$OUT" | tr -d ' ')"

# Bind private IP only. One GET then exit and unlink the tar.
nohup python3 - "$PRIV_IP" "$PORT" "$OUT" >/var/lib/tokenkey/host-config-pending/serve.log 2>&1 <<'PY' &
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
  "url": "http://${PRIV_IP}:${PORT}/prod-entry.tar.gz",
  "private_ip": "${PRIV_IP}",
  "port": int("${PORT}"),
  "sha256": "${SHA}",
  "bytes": int("${BYTES}"),
  "pending_path": "${OUT}",
}, ensure_ascii=False, indent=2))
PY
