#!/usr/bin/env bash
set -euo pipefail

TAG="${1:-${INPUT_TAG:-}}"
OVERRIDE="${2:-${INPUT_OVERRIDE:-false}}"
REPOSITORY="${GITHUB_REPOSITORY:-${GITHUB_REPOSITORY_OVERRIDE:-}}"
TOKEN="${GH_TOKEN:-${GITHUB_TOKEN:-}}"

if [[ -z "${TAG}" ]]; then
  echo "stage0_verify_ghcr_manifest: tag is required" >&2
  exit 1
fi
if [[ -z "${REPOSITORY}" ]]; then
  echo "stage0_verify_ghcr_manifest: GITHUB_REPOSITORY is required" >&2
  exit 1
fi
if [[ -z "${TOKEN}" ]]; then
  echo "stage0_verify_ghcr_manifest: GH_TOKEN or GITHUB_TOKEN is required" >&2
  exit 1
fi

repo_lower="$(printf '%s' "${REPOSITORY}" | tr '[:upper:]' '[:lower:]')"
reg_token="$(curl -sSL \
  -H "Authorization: Bearer ${TOKEN}" \
  "https://ghcr.io/token?scope=repository:${repo_lower}:pull" \
  | jq -r .token)"
if [[ -z "${reg_token}" || "${reg_token}" == "null" ]]; then
  echo "::error::failed to obtain GHCR registry token" >&2
  exit 1
fi

manifest="$(curl -sSL \
  -H "Authorization: Bearer ${reg_token}" \
  -H 'Accept: application/vnd.docker.distribution.manifest.list.v2+json' \
  -H 'Accept: application/vnd.oci.image.index.v1+json' \
  -H 'Accept: application/vnd.docker.distribution.manifest.v2+json' \
  -H 'Accept: application/vnd.oci.image.manifest.v1+json' \
  "https://ghcr.io/v2/${repo_lower}/manifests/${TAG}")"

if ! printf '%s' "${manifest}" | jq empty 2>/dev/null; then
  echo "::error::GHCR manifest for ${TAG} is not valid JSON; image probably does not exist" >&2
  printf '%s\n' "${manifest}" | head -c 500 >&2
  exit 1
fi

media="$(printf '%s' "${manifest}" | jq -r '.mediaType // empty')"
amd64_count="$(printf '%s' "${manifest}" | jq '[.manifests[]? | select(.platform.architecture=="amd64" and .platform.os=="linux")] | length')"
arm64_count="$(printf '%s' "${manifest}" | jq '[.manifests[]? | select(.platform.architecture=="arm64" and .platform.os=="linux")] | length')"
echo "mediaType=${media} amd64=${amd64_count} arm64=${arm64_count}"

is_multiarch=false
case "${media}" in
  application/vnd.docker.distribution.manifest.list.v2+json|application/vnd.oci.image.index.v1+json)
    if [[ "${amd64_count:-0}" -ge 1 && "${arm64_count:-0}" -ge 1 ]]; then
      is_multiarch=true
    fi
    ;;
esac

if [[ "${is_multiarch}" == "true" ]]; then
  echo "ok: ${TAG} is multi-arch (amd64 + arm64)"
else
  if [[ "${OVERRIDE}" == "true" ]]; then
    echo "::warning::${TAG} is single-arch but simple_release_override=true was passed; deploying anyway. If the target host is Graviton this WILL crash."
  else
    echo "::error::${TAG} is not a multi-arch manifest (mediaType=${media}, amd64=${amd64_count}, arm64=${arm64_count}). Re-run release.yml with simple_release=false, or pass simple_release_override=true only after verifying every host is amd64." >&2
    exit 1
  fi
fi
