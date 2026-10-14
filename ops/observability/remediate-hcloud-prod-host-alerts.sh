#!/usr/bin/env bash
# Existing HCloud fleet. Legacy filename retained for the prod entrypoint.
# Deliver with run-probe --with the shared alert
# script and prod-target-hetzner.json; never embed a second alert implementation.
# Default plan reads only. APPLY=yes-hcloud-prod-host-alerts installs the script
# and volume drop-in; the existing timer runs it on its normal schedule.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ALERT_SOURCE="$SCRIPT_DIR/tokenkey-disk-metrics-edge.sh"
PROD_MATRIX="$SCRIPT_DIR/prod-target-hetzner.json"
EDGE_MATRIX="$SCRIPT_DIR/edge-targets-hetzner.json"
ALERT_INSTALLED=/usr/local/bin/tokenkey-disk-metrics.sh
DROPIN_DIR=/etc/systemd/system/tokenkey-disk-metrics.service.d
BACKUP_ROOT=/var/backups/tokenkey-host-alerts
VOLUME_DEVICES=(/dev/disk/by-id/scsi-0HC_Volume_*)

fail() { echo "host-alerts: $*" >&2; exit 1; }

main() {
  local expected_host volume mounted_device backup stage unit_env
  local target="${ALERT_TARGET:-prod}"
  case "${APPLY:-}" in
    ""|yes-hcloud-host-alerts) ;;
    yes-hcloud-prod-host-alerts) [ "$target" = prod ] || fail 'prod APPLY requires prod target' ;;
    *) fail 'invalid APPLY value' ;;
  esac
  [ -f "$ALERT_SOURCE" ] && [ -f "$PROD_MATRIX" ] || fail 'missing --with shared alert script or prod matrix'
  read -r expected_host volume < <(python3 - "$PROD_MATRIX" "$EDGE_MATRIX" "$target" "$SCRIPT_DIR" <<'PY'
import json, sys
from pathlib import Path
sys.path.insert(0, sys.argv[4])
sys.path.insert(0, str(Path(sys.argv[4]).parents[0] / "stage0"))
if sys.argv[3] != "prod":
    from edge_routing_matrix import materialize_hetzner_target
    assert sys.argv[3].startswith("edge:")
    edge = sys.argv[3].split(":", 1)[1]
    matrix = json.loads(Path(sys.argv[2]).read_text())
    assert matrix["platform"] == "hetzner"
    target = materialize_hetzner_target(edge, matrix, matrix["targets"][edge])
    assert target["deployable"] is True
    print(target["instance_name"], "-")
    raise SystemExit(0)
with open(sys.argv[1], encoding="utf-8") as handle:
    matrix = json.load(handle)
target = matrix["target"]
assert matrix["platform"] == "hetzner" and matrix["role"] == "prod"
assert target["deployable"] is True
assert target["volume_mount"] == "/var/lib/tokenkey"
print(target["instance_name"], target["volume_mount"])
PY
  ) || fail 'invalid prod matrix'
  [ "$(hostname)" = "$expected_host" ] || fail 'host identity does not match current HCloud matrix'
  if [ "$target" = prod ]; then
  mountpoint -q "$volume" || fail 'prod data volume is not mounted'
  mounted_device="$(findmnt -n -o SOURCE --mountpoint "$volume")"
  # A mount of the root device or an unrelated filesystem is not the data volume.
  local device found_volume=0
  for device in "${VOLUME_DEVICES[@]}"; do
    if [ -e "$device" ] && [ "$(readlink -f "$device")" = "$(readlink -f "$mounted_device")" ]; then
      found_volume=1
      break
    fi
  done
  [ "$found_volume" = 1 ] || fail 'mounted device is not a Hetzner volume'
  fi
  [ -f "$ALERT_INSTALLED" ] || fail 'installed alert script is missing'
  systemctl cat tokenkey-disk-metrics.service >/dev/null
  systemctl show tokenkey-disk-metrics.service -p ExecStart --value | grep -Fq "path=$ALERT_INSTALLED ;" || fail 'unexpected alert service executable'
  systemctl is-enabled --quiet tokenkey-disk-metrics.timer || fail 'alert timer is not enabled'
  systemctl is-active --quiet tokenkey-disk-metrics.timer || fail 'alert timer is not active'
  bash -n "$ALERT_SOURCE"
  printf 'host=%s volume=%s source_sha256=' "$expected_host" "$volume"
  sha256sum "$ALERT_SOURCE" | cut -d ' ' -f1
  if [ -z "${APPLY:-}" ]; then
    echo 'mode=plan: replace shared alert script; bind data volume on prod; no writes or service execution'
    return
  fi
  [ "$(id -u)" = 0 ] || fail 'apply requires root'
  bash "$ALERT_SOURCE" --selftest
  install -d -m 0700 "$BACKUP_ROOT"
  backup="$(mktemp -d "$BACKUP_ROOT/$(date -u +%Y%m%dT%H%M%SZ).XXXXXX")"
  cp -a "$ALERT_INSTALLED" "$backup/tokenkey-disk-metrics.sh"
  if [ -d "$DROPIN_DIR" ]; then
    cp -a "$DROPIN_DIR" "$backup/drop-ins"
  fi
  # Print recovery location before the first live mutation, including on failure.
  printf 'backup=%s\n' "$backup"
  stage="$(mktemp "${ALERT_INSTALLED}.XXXXXX")"
  install -m 0755 "$ALERT_SOURCE" "$stage"
  mv -f "$stage" "$ALERT_INSTALLED"
  if [ "$target" = prod ]; then
  install -d -m 0755 "$DROPIN_DIR"
  stage="$(mktemp "$DROPIN_DIR/.hcloud-volume.XXXXXX")"
  printf '[Service]\nEnvironmentFile=-%s/.env\nEnvironment=TOKENKEY_DATA_VOLUME_PATH=%s\n' "$volume" "$volume" > "$stage"
  chmod 0644 "$stage"
  mv -f "$stage" "$DROPIN_DIR/hetzner-volume.conf"
  fi
  systemctl daemon-reload
  cmp -s "$ALERT_SOURCE" "$ALERT_INSTALLED" || fail 'installed source differs'
  if [ "$target" = prod ]; then
  unit_env="$(systemctl show tokenkey-disk-metrics.service -p Environment --value)"
  [[ " $unit_env " == *" TOKENKEY_DATA_VOLUME_PATH=$volume "* ]] || fail 'effective data-volume binding missing'
  fi
  systemctl is-active --quiet tokenkey-disk-metrics.timer || fail 'alert timer inactive after reload'
  echo 'mode=apply: installed and verified; service will run on its existing timer'
}

if [[ "${BASH_SOURCE[0]}" = "$0" ]]; then
  main "$@"
fi
