#!/bin/bash
# TokenKey Stage0 — prune local GHCR app image tags after each compose pull (inner script).
# Served from SSM .../ghcr-prune.b64 (base64); loader at /usr/local/bin/tokenkey-prune-ghcr-app-tags.sh
# fetches and execs this file. Keeps the most recent KEEP_N tags by image Created time, plus every
# tag for the image running in container "tokenkey".
#
# Source: deploy/aws/stage0/tokenkey-prune-ghcr-app-tags.sh — refresh via deploy/aws/stage0/build-cfn.sh.
# Optional: TOKENKEY_GHCR_KEEP_TAGS in /var/lib/tokenkey/.env (default 10).

set -euo pipefail

KEEP_N="${TOKENKEY_GHCR_KEEP_TAGS:-10}"
ENV_FILE="/var/lib/tokenkey/.env"

if [ ! -f "${ENV_FILE}" ]; then
  echo "tokenkey-prune-ghcr-app-tags: missing ${ENV_FILE}" >&2
  exit 0
fi

# shellcheck disable=SC1090
set -a
. "${ENV_FILE}"
set +a

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
