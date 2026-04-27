#!/usr/bin/env bash
# bump-new-api.sh — thin wrapper for bumping the pinned QuantumNous/new-api SHA.
#
# Usage:
#   bash scripts/bump-new-api.sh <sha>
#   bash scripts/bump-new-api.sh <sha> --no-test
#
# Default mode:
#   - updates .new-api-ref via scripts/sync-new-api.sh --bump
#   - syncs the sibling clone
#   - runs the fixed backend smoke set so the pin bump fails fast

set -euo pipefail

if [ "$#" -lt 1 ]; then
  sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
fi

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." &>/dev/null && pwd)"
PIN_FILE="${REPO_ROOT}/.new-api-ref"
TARGET_SHA=""
RUN_TESTS=1

run_backend_smoke() {
  echo ""
  echo "Running backend compile + compat smoke ..."
  (
    cd "${REPO_ROOT}/backend"
    go build ./...
    go test -tags=unit ./internal/engine -run 'TestOpenAICompatPlatforms|TestIsOpenAICompatPlatform|TestIsOpenAICompatPoolMember|TestBridgeEndpointEnabled' -count=1
    go test -tags=unit ./internal/relay/bridge -run 'TestDispatchVideoSubmit_VolcEngine_OK|TestDispatchVideoFetch_VolcEngine_OK|TestIsVideoSupportedChannelType_Truth' -count=1
    go test -tags=unit ./internal/service -run 'TestBridgeEndpointEnabled_Truth' -count=1
    go test -tags=unit ./internal/server/routes -run 'TestGatewayRoutesNewAPICompatPathsAreRegistered|TestGatewayRoutesVideoGenerationPathsAreRegistered' -count=1
  )
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --no-test)
      RUN_TESTS=0
      shift
      ;;
    -h|--help)
      sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      if [ -z "$TARGET_SHA" ]; then
        TARGET_SHA="$1"
        shift
      else
        echo "ERROR: unknown arg '$1'" >&2
        exit 1
      fi
      ;;
  esac
done

if ! [[ "$TARGET_SHA" =~ ^[0-9a-fA-F]{7,40}$ ]]; then
  echo "ERROR: expected a git SHA (7-40 hex chars), got '$TARGET_SHA'" >&2
  exit 1
fi

OLD_PIN=""
if [ -f "$PIN_FILE" ]; then
  OLD_PIN="$(tr -d '[:space:]' < "$PIN_FILE")"
fi

bash "${REPO_ROOT}/scripts/sync-new-api.sh" --bump "$TARGET_SHA"

if [ "$RUN_TESTS" -eq 1 ]; then
  run_backend_smoke
fi

NEW_PIN="$(tr -d '[:space:]' < "$PIN_FILE")"
SHORT_SHA="${NEW_PIN:0:12}"

echo ""
echo "Pinned new-api: ${OLD_PIN:-<unset>} -> ${NEW_PIN}"
if [ "$RUN_TESTS" -eq 0 ]; then
  echo "Backend smoke skipped (--no-test)."
fi
echo "Next steps:"
echo "  git diff -- .new-api-ref"
echo "  git add .new-api-ref"
echo "  git commit -m \"chore(deps): bump new-api to ${SHORT_SHA}\""
