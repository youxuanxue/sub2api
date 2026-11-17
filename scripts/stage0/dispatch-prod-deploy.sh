#!/usr/bin/env bash
# Canonical prod/cn-prod Stage0 dispatch — rolling deploy + warm workflows.
#
# Usage:
#   bash scripts/stage0/dispatch-prod-deploy.sh \
#     --operation deploy|cn-deploy|replay|smoke-only|warm|legacy-deploy --tag 1.2.3 \
#     --revision FULL_SHA \
#     [--replay-receipt SHA] [--replace-receipt SHA] [--ssot-models IDS] \
#     [--simple-release-override true|false] [--ref REF]
#
# --tag accepts X.Y.Z or vX.Y.Z; a leading v is stripped before workflow_dispatch
# (prod workflows require the bare image tag; they checkout refs/tags/v${tag}).
# See ops/stage0/normalize-deploy-tag.sh.
#
# Domain SSOT:
#   git release tag / release.yml  → vX.Y.Z
#   VERSION / GHCR / deploy inputs → X.Y.Z  (this script)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

OPERATION=""
TAG=""
REVISION=""
REPLAY_RECEIPT=""
REPLACE_RECEIPT=""
SSOT_MODELS=""
SIMPLE_OVERRIDE=""
WORKFLOW_REF=""

usage() {
  sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --operation) OPERATION="${2:-}"; shift 2 ;;
    --tag) TAG="${2:-}"; shift 2 ;;
    --revision) REVISION="${2:-}"; shift 2 ;;
    --replay-receipt) REPLAY_RECEIPT="${2:-}"; shift 2 ;;
    --replace-receipt) REPLACE_RECEIPT="${2:-}"; shift 2 ;;
    --ssot-models) SSOT_MODELS="${2:-}"; shift 2 ;;
    --simple-release-override) SIMPLE_OVERRIDE="${2:-}"; shift 2 ;;
    --ref) WORKFLOW_REF="${2:?--ref requires a Git ref}"; shift 2 ;;
    -h|--help) usage ;;
    *)
      echo "dispatch-prod-deploy: unknown argument: $1" >&2
      usage
      ;;
  esac
done

if [[ -z "${OPERATION}" || -z "${TAG}" ]]; then
  echo "dispatch-prod-deploy: --operation and --tag are required" >&2
  usage
fi

case "${OPERATION}" in
  deploy|cn-deploy|replay|smoke-only|warm|legacy-deploy) ;;
  *)
    echo "dispatch-prod-deploy: unsupported operation=${OPERATION} (want deploy|cn-deploy|replay|smoke-only|warm|legacy-deploy)" >&2
    exit 1
    ;;
esac

if [[ -n "${SIMPLE_OVERRIDE}" ]]; then
  case "${SIMPLE_OVERRIDE}" in
    true|false) ;;
    *)
      echo "dispatch-prod-deploy: --simple-release-override must be true or false" >&2
      exit 1
      ;;
  esac
fi

if [[ "${OPERATION}" != "legacy-deploy" && -n "${REPLAY_RECEIPT}" ]]; then
  echo "dispatch-prod-deploy: --replay-receipt is only valid with --operation legacy-deploy (promote path)" >&2
  exit 1
fi
if [[ "${OPERATION}" != "replay" && -n "${REPLACE_RECEIPT}" ]]; then
  echo "dispatch-prod-deploy: --replace-receipt is only valid with --operation replay" >&2
  exit 1
fi
if [[ "${OPERATION}" != "smoke-only" && -n "${SSOT_MODELS}" ]]; then
  echo "dispatch-prod-deploy: --ssot-models is only valid with --operation smoke-only" >&2
  exit 1
fi
if [[ "${OPERATION}" == "warm" && ( -n "${REPLAY_RECEIPT}" || -n "${REPLACE_RECEIPT}" || -n "${SSOT_MODELS}" ) ]]; then
  echo "dispatch-prod-deploy: warm only accepts --tag / --revision / --simple-release-override / --ref" >&2
  exit 1
fi

if ! TAG="$(bash ops/stage0/normalize-deploy-tag.sh "${TAG}")"; then
  echo "dispatch-prod-deploy: invalid --tag (want X.Y.Z or vX.Y.Z, optionally -rc.N/-beta.N)" >&2
  exit 1
fi

if [[ "${OPERATION}" == "deploy" || "${OPERATION}" == "cn-deploy" || "${OPERATION}" == "warm" ]]; then
  if [[ ! "${REVISION}" =~ ^[0-9a-f]{40}$ ]]; then
    echo "dispatch-prod-deploy: --revision FULL_SHA (40 lowercase hex chars) is required with --operation ${OPERATION}" >&2
    exit 1
  fi
fi

WORKFLOW=""
GH_ARGS=()
case "${OPERATION}" in
  warm)
    WORKFLOW="warm-image-stage0.yml"
    GH_ARGS=(
      workflow run "${WORKFLOW}"
      -f "tag=${TAG}"
      -f "revision=${REVISION}"
    )
    if [[ -n "${SIMPLE_OVERRIDE}" ]]; then
      GH_ARGS+=(-f "simple_release_override=${SIMPLE_OVERRIDE}")
    fi
    ;;
  deploy)
    WORKFLOW="prod-rolling-deploy.yml"
    GH_ARGS=(
      workflow run "${WORKFLOW}"
      -f "tag=${TAG}"
      -f "revision=${REVISION}"
      -f "confirm=rolling-prod-${TAG}"
    )
    ;;
  cn-deploy)
    WORKFLOW="cn-prod-rolling-deploy.yml"
    GH_ARGS=(
      workflow run "${WORKFLOW}"
      -f "tag=${TAG}"
      -f "revision=${REVISION}"
      -f "confirm=rolling-cn-prod-${TAG}"
    )
    ;;
  legacy-deploy|replay|smoke-only)
    # M6 (prod): live fleet is single-container rolling. legacy blue/green stays
    # behind an explicit emergency unlock only.
    if [[ "${OPERATION}" == "legacy-deploy" && "${STAGE0_ALLOW_LEGACY_BLUEGREEN:-}" != "1" ]]; then
      echo "dispatch-prod-deploy: legacy-deploy retired for live prod; use --operation deploy (rolling)" >&2
      echo "  set STAGE0_ALLOW_LEGACY_BLUEGREEN=1 only for approved emergency recovery" >&2
      exit 2
    fi
    WORKFLOW="deploy-stage0.yml"
    GH_ARGS=(
      workflow run "${WORKFLOW}"
      -f "operation=$([[ "${OPERATION}" == "legacy-deploy" ]] && printf deploy || printf %s "${OPERATION}")"
      -f "tag=${TAG}"
    )
    if [[ -n "${REPLAY_RECEIPT}" ]]; then
      GH_ARGS+=(-f "replay_receipt=${REPLAY_RECEIPT}")
    fi
    if [[ -n "${REPLACE_RECEIPT}" ]]; then
      GH_ARGS+=(-f "replace_receipt=${REPLACE_RECEIPT}")
    fi
    if [[ -n "${SSOT_MODELS}" ]]; then
      GH_ARGS+=(-f "ssot_models=${SSOT_MODELS}")
    fi
    if [[ -n "${SIMPLE_OVERRIDE}" ]]; then
      GH_ARGS+=(-f "simple_release_override=${SIMPLE_OVERRIDE}")
    fi
    ;;
esac

if [[ -n "${WORKFLOW_REF}" ]]; then
  GH_ARGS+=(--ref "${WORKFLOW_REF}")
fi

echo "dispatch-prod-deploy: workflow=${WORKFLOW} op=${OPERATION} tag=${TAG} revision=${REVISION:-n/a} ref=${WORKFLOW_REF:-default}"
gh "${GH_ARGS[@]}"
