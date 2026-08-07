#!/usr/bin/env bash
# Verify each published release image platform contains both required executables.
set -euo pipefail

IMAGE="${1:-}"
PLATFORMS_CSV="${2:-linux/amd64,linux/arm64}"

if [ -z "${IMAGE}" ]; then
  echo "usage: $0 <image:tag> [platforms-csv]" >&2
  exit 2
fi

IFS=',' read -r -a platforms <<<"${PLATFORMS_CSV}"
if [ "${#platforms[@]}" -eq 0 ]; then
  echo "release image platforms cannot be empty" >&2
  exit 2
fi

for platform in "${platforms[@]}"; do
  case "${platform}" in
    linux/amd64|linux/arm64) ;;
    *)
      echo "unsupported release image platform: ${platform}" >&2
      exit 2
      ;;
  esac
  echo "Verifying ${IMAGE} on ${platform}"
  pulled=false
  for attempt in 1 2 3 4 5 6; do
    if docker pull --platform "${platform}" "${IMAGE}"; then
      pulled=true
      break
    fi
    if [ "${attempt}" -lt 6 ]; then
      sleep 10
    fi
  done
  if [ "${pulled}" != true ]; then
    echo "failed to pull ${IMAGE} for ${platform}" >&2
    exit 1
  fi
  docker run --rm --pull=never --platform "${platform}" --entrypoint /bin/sh "${IMAGE}" -ec \
    'test -x /app/sub2api && test -x /app/qa-archive'
done
