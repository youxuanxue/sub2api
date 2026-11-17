#!/usr/bin/env bash
# Run on a prod app host: crane-legacy export the release image and upload to Beijing COS.
# Companions expected in /tmp: image_distribution.py, image_archive.py, image-distribution.json
# Env: RELEASE_TAG, RELEASE_REVISION, GHCR_REPOSITORY, TENCENTCLOUD_SECRET_ID/KEY
# Optional: GHCR_TOKEN (+ GHCR_USER) for crane auth.
set -euo pipefail
umask 077

: "${RELEASE_TAG:?}"
: "${RELEASE_REVISION:?}"
: "${GHCR_REPOSITORY:?}"
: "${TENCENTCLOUD_SECRET_ID:?}"
: "${TENCENTCLOUD_SECRET_KEY:?}"

ROOT=/tmp/cn-image-publish-$$
mkdir -p "$ROOT"
cleanup() { rm -rf "$ROOT"; }
trap cleanup EXIT

cp /tmp/image_distribution.py /tmp/image_archive.py /tmp/image-distribution.json "$ROOT/"
cd "$ROOT"

if ! command -v crane >/dev/null 2>&1; then
  # Prod app hosts do not ship crane; bootstrap a pinned static binary into /tmp.
  # SHA256 from go-containerregistry v0.22.1 release checksums.txt.
  CRANE_VER=v0.22.1
  CRANE_TGZ_SHA256=0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0
  CRANE_TGZ="$ROOT/go-containerregistry_Linux_x86_64.tar.gz"
  curl -fsSL "https://github.com/google/go-containerregistry/releases/download/${CRANE_VER}/go-containerregistry_Linux_x86_64.tar.gz" \
    -o "$CRANE_TGZ"
  actual="$(sha256sum "$CRANE_TGZ" | awk '{print $1}')"
  if [ "$actual" != "$CRANE_TGZ_SHA256" ]; then
    echo "crane tarball sha256 mismatch: got ${actual} want ${CRANE_TGZ_SHA256}" >&2
    exit 1
  fi
  tar -xz -C /tmp -f "$CRANE_TGZ" crane
  chmod 0755 /tmp/crane
  export PATH="/tmp:${PATH}"
fi
command -v crane >/dev/null 2>&1 || { echo "crane unavailable after bootstrap" >&2; exit 1; }
# Prod hosts already ship boto3; avoid python3 -m pip (ensurepip/pip absent).
python3 -c 'import boto3' || { echo "boto3 missing on prod host" >&2; exit 1; }

if [ -n "${GHCR_TOKEN:-}" ]; then
  user="${GHCR_USER:-${GITHUB_REPOSITORY_OWNER:-tokenkey}}"
  printf '%s' "$GHCR_TOKEN" | crane auth login ghcr.io -u "$user" --password-stdin >/dev/null
fi

# CLI action name publish-docker is a compatibility alias; implementation is crane-legacy.
python3 image_distribution.py publish-docker \
  --tag "$RELEASE_TAG" \
  --revision "$RELEASE_REVISION" \
  --repository "$GHCR_REPOSITORY"
echo "publish_cn_from_prod_ok"
