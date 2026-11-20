#!/usr/bin/env bash
# Read-only list of COS prod/host-config objects on a host that has
# /etc/tokenkey/cos-backup.env (data machine; same key as pgBackRest).
#   bash ops/observability/run-probe.sh --target prod-data \
#     --script ops/archive/probe-list-host-config.sh
set -euo pipefail

LIST="/tmp/list_host_config.py"
IDENT="/tmp/cos_archive_identity.py"
SETTINGS="/tmp/cos-archive.json"
for f in "$LIST" "$IDENT" "$SETTINGS"; do
  if [ ! -f "$f" ]; then
    base="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    LIST="$base/list_host_config.py"
    IDENT="$base/cos_archive_identity.py"
    SETTINGS="$base/cos-archive.json"
    break
  fi
done

if [ ! -f "$LIST" ] || [ ! -f "$IDENT" ] || [ ! -f "$SETTINGS" ]; then
  echo '{"ok":false,"reason":"list_host_config companions missing on remote"}'
  exit 2
fi

# Companions share /tmp; point settings + identity via env used by the modules.
export TK_COS_ARCHIVE_SETTINGS="$SETTINGS"
# list_host_config imports cos_archive_identity from its directory; copy layout.
WORKDIR="$(mktemp -d /tmp/tk-host-config-list.XXXXXX)"
trap 'rm -rf "$WORKDIR"' EXIT
cp -a "$LIST" "$IDENT" "$SETTINGS" "$WORKDIR/"
python3 "$WORKDIR/list_host_config.py" --prefix "${HOST_CONFIG_PREFIX:-}"
