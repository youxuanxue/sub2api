#!/usr/bin/env bash
# On prod-data: pull plaintext entry pack from private URL and upload to COS.
#
#   bash ops/observability/run-probe.sh --target prod-data \
#     --script ops/archive/probe-data-fetch-upload-entry-host-config.sh \
#     --env TK_HOST_CONFIG_APPLY=1 \
#     --env TK_HOST_CONFIG_URL=http://10.4.0.8:8765/... \
#     --env TK_HOST_CONFIG_SHA256=<sha>
set -euo pipefail

if [ "${TK_HOST_CONFIG_APPLY:-}" != "1" ]; then
  echo '{"ok":false,"reason":"refused: set TK_HOST_CONFIG_APPLY=1"}'
  exit 64
fi
URL="${TK_HOST_CONFIG_URL:?TK_HOST_CONFIG_URL required}"
WANT_SHA="${TK_HOST_CONFIG_SHA256:?TK_HOST_CONFIG_SHA256 required}"
LABEL="${TK_HOST_CONFIG_LABEL:-prod-entry}"

WORKDIR="$(mktemp -d /tmp/tk-host-config-fetch.XXXXXX)"
trap 'rm -rf "$WORKDIR"' EXIT

for f in upload_host_config.py cos_archive_identity.py cos-archive.json; do
  if [ -f "/tmp/$f" ]; then
    cp -a "/tmp/$f" "$WORKDIR/"
  fi
done
BASE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
for f in upload_host_config.py cos_archive_identity.py cos-archive.json; do
  [ -f "$WORKDIR/$f" ] || cp -a "$BASE/$f" "$WORKDIR/"
done

OUT="$WORKDIR/prod-entry.tar.gz"
curl -fsS --max-time 60 "$URL" -o "$OUT"
GOT_SHA="$(python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$OUT")"
if [ "$GOT_SHA" != "$WANT_SHA" ]; then
  python3 -c 'import json,sys; print(json.dumps({"ok":False,"reason":"sha256 mismatch","want":sys.argv[1],"got":sys.argv[2]}))' "$WANT_SHA" "$GOT_SHA"
  exit 3
fi

export TK_COS_ARCHIVE_SETTINGS="$WORKDIR/cos-archive.json"
python3 "$WORKDIR/upload_host_config.py" --file "$OUT" --label "$LABEL" >"$WORKDIR/plan.json"
python3 "$WORKDIR/upload_host_config.py" --file "$OUT" --label "$LABEL" --apply >"$WORKDIR/receipt.json"
python3 -c '
import json, pathlib
wd = pathlib.Path("'"$WORKDIR"'")
print(json.dumps({
  "ok": True,
  "sha256": "'"$GOT_SHA"'",
  "label": "'"$LABEL"'",
  "plan": json.loads((wd / "plan.json").read_text()),
  "receipt": json.loads((wd / "receipt.json").read_text()),
}, ensure_ascii=False, indent=2))
'
