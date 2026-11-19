#!/usr/bin/env bash
# Read-only inventory of entry TLS materials on the remote host.
# Deliver with:
#   bash ops/observability/run-probe.sh --target prod --node entry \
#     --script ops/archive/probe-entry-host-config-inventory.sh
# Never packs or uploads. Companions land in /tmp/<basename>.
set -euo pipefail

ROOT="${TK_ENTRY_HOST_ROOT:-/var/lib/tokenkey}"
PACK="/tmp/pack_entry_host_config.py"
if [ ! -f "$PACK" ]; then
  PACK="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pack_entry_host_config.py"
fi
if [ ! -f "$PACK" ]; then
  echo '{"ok":false,"reason":"pack_entry_host_config.py missing on remote"}'
  exit 2
fi

export TK_ENTRY_HOST_ROOT="$ROOT"
python3 "$PACK" inventory
