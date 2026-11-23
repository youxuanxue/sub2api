#!/usr/bin/env bash
# Write-capable: hot-patch live QA retention soft-defer + schedule stagger.
# Delivered via run-probe.sh with companion sources from the repo:
#   --with ops/qa/force_retention.py \
#   --with ops/qa/coordinator_host.py \
#   --with ops/qa/data_machine_coordinator.py \
#   --with ops/qa/install_coordinator.py
#
# Requires APPLY=yes-qa-retention-backup-defer
#
# Why not install_coordinator.py --apply:
#   installer refuses to replace existing units / existing content-addressed
#   bundles. This remediator patches the *active* ExecStart bundle in place and
#   rewrites tokenkey-qa-retention.timer only.
#
# Scope:
#   - Overwrite force_retention.py / coordinator_host.py /
#     data_machine_coordinator.py in the active bundle (BackupDeferred → exit 0)
#   - Replace retention timer from install_coordinator.retention_units SSOT
#   - daemon-reload; reset-failed retention; restart retention.timer
# Does NOT touch postgres-backup calendar, coordinator.timer cadence, or gateway.
set -euo pipefail

APPLY="${APPLY:-}"
EXPECT_APPLY=yes-qa-retention-backup-defer
SRC_DIR="${SRC_DIR:-/tmp}"
TIMER_DST=/etc/systemd/system/tokenkey-qa-retention.timer
WANTED_CALENDAR='OnCalendar=*-*-* *:00,10,20,40,50:00 UTC'

need_root() {
  if [[ "$(id -u)" -ne 0 ]]; then
    echo '{"ok":false,"error":"root required"}' >&2
    exit 1
  fi
}

active_bundle() {
  local exec
  exec="$(systemctl show tokenkey-qa-retention.service -p ExecStart --value 2>/dev/null || true)"
  # argv contains ... /usr/local/lib/tokenkey/qa-coordinator/<hash>/force_retention.py
  python3 - <<'PY' "$exec"
import re,sys
text=sys.argv[1]
m=re.search(r"(/usr/local/lib/tokenkey/qa-coordinator/[0-9a-f]+)/force_retention\.py", text)
print(m.group(1) if m else "")
PY
}

file_sha() {
  sha256sum "$1" 2>/dev/null | awk '{print $1}'
}

retention_timer_body() {
  # SSOT: extract tokenkey-qa-retention.timer from install_coordinator.py text
  # (avoid importing the module — it pulls sibling QA packages not uploaded here).
  python3 - <<'PY' "$SRC_DIR/install_coordinator.py"
import re, sys
from pathlib import Path
text = Path(sys.argv[1]).read_text()
match = re.search(r'"tokenkey-qa-retention\.timer":\s*"""(.*?)"""', text, re.S)
if not match:
    raise SystemExit("tokenkey-qa-retention.timer missing from install_coordinator.py")
print(match.group(1), end="")
PY
}

echo "host=$(hostname -s 2>/dev/null || hostname)"
echo "now_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "apply_requested=$([[ $APPLY == "$EXPECT_APPLY" ]] && echo yes || echo no)"

if ! systemctl cat tokenkey-qa-retention.service >/dev/null 2>&1; then
  echo '{"ok":false,"error":"tokenkey-qa-retention.service missing on this host"}'
  exit 0
fi

BUNDLE="$(active_bundle)"
if [[ -z "$BUNDLE" || ! -d "$BUNDLE" ]]; then
  echo "{\"ok\":false,\"error\":\"cannot resolve active retention bundle\",\"exec\":\"$(systemctl show tokenkey-qa-retention.service -p ExecStart --value | tr -d '\n')\"}"
  exit 1
fi
echo "active_bundle=${BUNDLE}"

for name in force_retention.py coordinator_host.py data_machine_coordinator.py install_coordinator.py; do
  src="${SRC_DIR}/${name}"
  if [[ ! -f "$src" ]]; then
    echo "{\"ok\":false,\"error\":\"missing companion ${src}; pass --with ops/qa/${name}\"}"
    exit 1
  fi
done
PATCH_NAMES=(force_retention.py coordinator_host.py data_machine_coordinator.py)
# App bundles also run source_worker over SSH; patch when present.
if [[ -f "${BUNDLE}/source_worker.py" ]]; then
  if [[ ! -f "${SRC_DIR}/source_worker.py" ]]; then
    echo "{\"ok\":false,\"error\":\"missing companion ${SRC_DIR}/source_worker.py; pass --with ops/qa/source_worker.py\"}"
    exit 1
  fi
  PATCH_NAMES+=(source_worker.py)
fi
for name in "${PATCH_NAMES[@]}"; do
  dst="${BUNDLE}/${name}"
  if [[ ! -f "$dst" ]]; then
    echo "{\"ok\":false,\"error\":\"missing live ${dst}\"}"
    exit 1
  fi
  echo "src_${name}_sha=$(file_sha "${SRC_DIR}/${name}")"
  echo "dst_${name}_sha=$(file_sha "$dst")"
  echo "dst_${name}_same=$([[ $(file_sha "${SRC_DIR}/${name}") == $(file_sha "$dst") ]] && echo yes || echo no)"
done

TIMER_BODY="$(retention_timer_body)"
if ! grep -Fqx "$WANTED_CALENDAR" <<<"$TIMER_BODY"; then
  echo '{"ok":false,"error":"install_coordinator retention timer missing wanted OnCalendar"}'
  exit 1
fi

echo "--- live timer ---"
sed -n '1,20p' "$TIMER_DST" 2>/dev/null || echo "(missing timer file)"
echo "timer_has_wanted_calendar=$(grep -Fqx "$WANTED_CALENDAR" "$TIMER_DST" 2>/dev/null && echo yes || echo no)"
echo "timer_has_5min=$(grep -q 'OnUnitInactiveSec=5min' "$TIMER_DST" 2>/dev/null && echo yes || echo no)"
echo "retention_result=$(systemctl show tokenkey-qa-retention.service -p Result --value 2>/dev/null || true)"
echo "retention_active=$(systemctl is-active tokenkey-qa-retention.service 2>/dev/null || true)"
echo "next_retention=$(systemctl show tokenkey-qa-retention.timer -p NextElapseUSecRealtime --value 2>/dev/null || true)"

if [[ "$APPLY" != "$EXPECT_APPLY" ]]; then
  echo "=== dry-run ==="
  echo "{\"ok\":true,\"apply\":false,\"bundle\":\"${BUNDLE}\",\"would_patch_py\":true,\"would_rewrite_timer\":true,\"hint\":\"set APPLY=${EXPECT_APPLY}\"}"
  exit 0
fi

need_root
echo "=== apply: patch active bundle python ==="
for name in "${PATCH_NAMES[@]}"; do
  install -m 0644 "${SRC_DIR}/${name}" "${BUNDLE}/${name}"
  echo "patched ${BUNDLE}/${name} sha=$(file_sha "${BUNDLE}/${name}")"
done

# Confirm soft-defer markers landed.
python3 - <<PY
from pathlib import Path
bundle = Path("$BUNDLE")
dm = (bundle / "data_machine_coordinator.py").read_text()
ch = (bundle / "coordinator_host.py").read_text()
fr = (bundle / "force_retention.py").read_text()
assert "class BackupDeferred" in dm, "BackupDeferred missing"
assert "class ArchiveDeferred" in dm, "ArchiveDeferred missing"
assert "def subprocess_detail" in dm, "subprocess_detail missing"
assert "def lifecycle_lock_busy" in dm, "lifecycle_lock_busy missing"
assert "raise policy.BackupDeferred" in ch, "soft defer raise missing"
assert "except policy.ArchiveDeferred" in ch, "archive soft defer missing"
assert "lifecycle_lock_busy" in ch, "QAMA lock busy soft defer missing"
assert "except policy.BackupDeferred" in fr, "force_retention soft exit missing"
assert "backup timer next elapse missing" in ch
assert 'raise policy.CoordinatorError("QA database work deferred near scheduled backup")' not in ch
sw = bundle / "source_worker.py"
if sw.exists():
    text = sw.read_text()
    assert "ArchiveDeferred" in text, "source_worker ArchiveDeferred missing"
    assert "subprocess_detail" in text, "source_worker subprocess_detail missing"
print("markers_ok=yes")
PY

echo "=== apply: rewrite retention timer from install_coordinator SSOT ==="
printf '%s\n' "$TIMER_BODY" >"$TIMER_DST"
printf '%s\n' "$TIMER_BODY" >"${BUNDLE}/tokenkey-qa-retention.timer"
chmod 0644 "$TIMER_DST" "${BUNDLE}/tokenkey-qa-retention.timer"
systemd-analyze verify "$TIMER_DST" "${BUNDLE}/tokenkey-qa-retention.service" 2>/dev/null \
  || systemd-analyze verify "$TIMER_DST"

systemctl daemon-reload
systemctl reset-failed tokenkey-qa-retention.service 2>/dev/null || true
systemctl restart tokenkey-qa-retention.timer

echo "=== after ==="
echo "timer_has_wanted_calendar=$(grep -Fqx "$WANTED_CALENDAR" "$TIMER_DST" && echo yes || echo no)"
echo "timer_has_5min=$(grep -q 'OnUnitInactiveSec=5min' "$TIMER_DST" && echo yes || echo no)"
systemctl show tokenkey-qa-retention.timer --property=ActiveState,UnitFileState,NextElapseUSecRealtime,LastTriggerUSec
systemctl show tokenkey-qa-retention.service --property=ActiveState,Result
python3 - <<PY
import sys
from pathlib import Path
bundle = Path("$BUNDLE")
sys.path.insert(0, str(bundle))
import data_machine_coordinator as policy
import force_retention
assert issubclass(policy.BackupDeferred, Exception)
print("import_ok=yes")
PY

echo "{\"ok\":true,\"apply\":true,\"bundle\":\"${BUNDLE}\"}"
echo DONE
