#!/usr/bin/env bash
# Verify-only: confirm tokenkey-qa-cleanup Result=success on layout-exception hosts.
# Does not patch the host. Repair owner is ops/qa/local_maintenance_host.py
# (resolve_postgres_runtime + app password fallback + symlink-aware HOT_DATA);
# re-ship that file via the QA install path when this check fails.
set -euo pipefail

echo "=== unit ==="
systemctl show tokenkey-qa-cleanup.service -p Result -p ExecMainStatus -p ActiveState --value || true
echo "=== last receipt ==="
sudo python3 - <<'PY'
import json
from pathlib import Path
p = Path('/var/lib/tokenkey/qa-local/cleanup.json')
if not p.exists():
    print('missing cleanup.json')
else:
    data = json.loads(p.read_text())
    print(json.dumps({k: data.get(k) for k in ('ok','phase','error','observed_at')}, sort_keys=True))
PY
result="$(systemctl show tokenkey-qa-cleanup.service -p Result --value 2>/dev/null || echo unknown)"
if [ "$result" != "success" ]; then
  echo "::error::tokenkey-qa-cleanup Result=$result — re-ship ops/qa/local_maintenance_host.py"
  journalctl -u tokenkey-qa-cleanup.service -n 30 --no-pager || true
  exit 1
fi
echo "verdict=qa_cleanup_ok"
