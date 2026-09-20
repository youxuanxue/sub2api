#!/usr/bin/env bash
# Normalize a Stage0 deploy tag for workflow_dispatch inputs.
#
# Domain SSOT (two forms on purpose — do not collapse to one literal):
#   git release tag / release.yml → vX.Y.Z
#   VERSION / GHCR / deploy+warm workflow inputs → X.Y.Z
#
# Workflows checkout refs/tags/v${tag}, so inputs must stay bare. Local dispatch
# scripts (dispatch-prod-deploy / dispatch-edge-deploy / rollout-edges) call this
# helper so agents may pass either form; validate-deploy-tag.sh still rejects a
# leading v on the workflow side.
#
# Usage: normalize-deploy-tag.sh <tag>
# Prints the bare X.Y.Z(-rc.N|-beta.N) tag on stdout; exits 1 on empty/invalid.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TAG="${1:-}"

if [ -z "$TAG" ]; then
  echo "normalize-deploy-tag: no tag provided" >&2
  exit 1
fi

case "$TAG" in
  v*) TAG="${TAG#v}" ;;
esac

# Reuse the workflow-side format gate so local dispatch and CI never diverge.
bash "${SCRIPT_DIR}/validate-deploy-tag.sh" "$TAG" >/dev/null
printf '%s\n' "$TAG"
