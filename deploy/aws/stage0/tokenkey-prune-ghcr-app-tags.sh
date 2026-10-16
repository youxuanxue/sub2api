#!/bin/bash
# Prune GHCR tags; SSM core owned here. Regenerate via build-cfn.sh.
# TOKENKEY_GHCR_KEEP_TAGS defaults to 10.

set -euo pipefail

KEEP_N="${TOKENKEY_GHCR_KEEP_TAGS:-10}"
ENV_FILE="${TOKENKEY_PRUNE_ENV_FILE:-/var/lib/tokenkey/.env}"

if [ ! -f "${ENV_FILE}" ]; then
  echo "tokenkey-prune-ghcr-app-tags: missing ${ENV_FILE}" >&2
  exit 0
fi

# Dotenv is data, not shell: migration CIDRs contain spaces and credentials
# may contain shell syntax. Read only the image reference needed by this job.
TOKENKEY_IMAGE="$(python3 - "${ENV_FILE}" <<'PYENV'
from pathlib import Path
import sys
value = ""
for line in Path(sys.argv[1]).read_text().splitlines():
    key, sep, candidate = line.partition("=")
    if sep and key.strip() == "TOKENKEY_IMAGE":
        value = candidate.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
print(value)
PYENV
)"

if [ -z "${TOKENKEY_IMAGE:-}" ]; then
  echo "tokenkey-prune-ghcr-app-tags: TOKENKEY_IMAGE empty" >&2
  exit 0
fi

REPO="${TOKENKEY_IMAGE%:*}"
if [ -z "${REPO}" ] || [ "${REPO}" = "${TOKENKEY_IMAGE}" ]; then
  echo "tokenkey-prune-ghcr-app-tags: could not parse repo from TOKENKEY_IMAGE" >&2
  exit 0
fi

RUNNING_ID=""
if docker inspect tokenkey >/dev/null 2>&1; then
  RUNNING_ID="$(docker inspect -f '{{.Image}}' tokenkey)"
fi

sorted="$(mktemp)"
keepf="$(mktemp)"
trap 'rm -f "${sorted}" "${keepf}"' EXIT

while read -r ref; do
  [ -z "$ref" ] && continue
  case "$ref" in *'<none>'*) continue ;; esac
  ctime="$(docker inspect -f '{{.Created}}' "$ref" 2>/dev/null || echo '1970-01-01T00:00:00Z')"
  iid="$(docker inspect -f '{{.Id}}' "$ref" 2>/dev/null || true)"
  printf '%s\t%s\t%s\n' "$ctime" "$ref" "$iid"
done < <(docker images "${REPO}" --format '{{.Repository}}:{{.Tag}}' 2>/dev/null || true) \
  | LC_ALL=C sort -t "$(printf '\t')" -k1,1r >"${sorted}"

: >"${keepf}"

while IFS="$(printf '\t')" read -r _cref ref iid; do
  [ -z "${ref:-}" ] && continue
  if [ -n "${RUNNING_ID}" ] && [ "$iid" = "${RUNNING_ID}" ]; then
    echo "$ref" >>"${keepf}"
  fi
done <"${sorted}"

while IFS="$(printf '\t')" read -r _cref ref _iid; do
  [ -z "${ref:-}" ] && continue
  kcount=0
  if [ -s "${keepf}" ]; then
    kcount=$(wc -l <"${keepf}" | tr -d ' ')
  fi
  [ "${kcount}" -ge "${KEEP_N}" ] && break
  if ! grep -Fxq "$ref" "${keepf}" 2>/dev/null; then
    echo "$ref" >>"${keepf}"
  fi
done <"${sorted}"

while read -r ref; do
  [ -z "$ref" ] && continue
  case "$ref" in *'<none>'*) continue ;; esac
  if ! grep -Fxq "$ref" "${keepf}" 2>/dev/null; then
    docker rmi "$ref" 2>/dev/null || true
  fi
done < <(docker images "${REPO}" --format '{{.Repository}}:{{.Tag}}' 2>/dev/null || true)

exit 0
