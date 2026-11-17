#!/usr/bin/env bash
# Run on a prod app host: docker-save the release image and upload to Beijing COS.
# Companions expected in /tmp: image_distribution.py, image_archive.py, image-distribution.json
# Env: RELEASE_TAG, RELEASE_REVISION, GHCR_REPOSITORY, TENCENTCLOUD_SECRET_ID/KEY
# Optional: GHCR_TOKEN (+ GHCR_USER) for docker/crane auth; IMAGE_REF override.
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
  echo "crane missing on prod host" >&2
  exit 1
fi
python3 -m pip install -q 'boto3>=1.34' >/dev/null

if [ -n "${GHCR_TOKEN:-}" ]; then
  user="${GHCR_USER:-${GITHUB_REPOSITORY_OWNER:-tokenkey}}"
  printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "$user" --password-stdin >/dev/null
  printf '%s' "$GHCR_TOKEN" | crane auth login ghcr.io -u "$user" --password-stdin >/dev/null
fi

IMAGE_REF="${IMAGE_REF:-}"
# Prefer domestic local tag when prod warm already landed the release.
if [ -z "$IMAGE_REF" ]; then
  short="${RELEASE_REVISION:0:9}"
  candidate="tokenkey-domestic/tokenkey:${RELEASE_TAG}-${short}"
  if docker image inspect "$candidate" >/dev/null 2>&1; then
    IMAGE_REF="$candidate"
  fi
fi

ARGS=(publish-docker --tag "$RELEASE_TAG" --revision "$RELEASE_REVISION" --repository "$GHCR_REPOSITORY")
if [ -n "$IMAGE_REF" ]; then
  ARGS+=(--image "$IMAGE_REF")
fi

python3 image_distribution.py "${ARGS[@]}"
echo "publish_cn_from_prod_ok"
