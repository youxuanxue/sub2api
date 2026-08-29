#!/usr/bin/env bash
# Remote Edge infra smoke. Delivered through ops/observability/run-probe.sh,
# which places the canonical app-container resolver beside this script.
set -euo pipefail

TOKENKEY_ROOT="${TOKENKEY_ROOT:-/var/lib/tokenkey}"
TK_DOCKER="${TK_DOCKER:-sudo docker}"

resolver=""
for candidate in \
  "${TK_LIB_DIR:-$(dirname "$0")}/resolve-app-container.sh" \
  "$(dirname "$0")/../lib/resolve-app-container.sh"; do
  if [ -f "$candidate" ]; then
    resolver="$candidate"
    break
  fi
done
if [ -z "$resolver" ]; then
  echo "tk_edge_post_deploy_smoke: canonical app-container resolver not found" >&2
  exit 1
fi
# shellcheck source=../lib/resolve-app-container.sh
source "$resolver"

tk_docker() {
  # shellcheck disable=SC2086  # TK_DOCKER may legitimately be "sudo docker".
  $TK_DOCKER "$@"
}

cd "$TOKENKEY_ROOT"
tk_docker compose version
tk_docker compose -f docker-compose.yml --env-file .env ps

if ! app_container="$(tk_resolve_app_container auto)"; then
  echo "tk_edge_post_deploy_smoke: active app container unresolved" >&2
  exit 1
fi
if ! qa_cap="$(tk_docker exec "$app_container" printenv QA_CAPTURE_ENABLED 2>/dev/null)"; then
  qa_cap="missing"
fi
echo "tk_edge_post_deploy_smoke: container=${app_container} QA_CAPTURE_ENABLED=${qa_cap}"
if [ "$qa_cap" != "false" ]; then
  echo "tk_edge_post_deploy_smoke: edge QA capture must be disabled (QA_CAPTURE_ENABLED=false)" >&2
  exit 1
fi

tk_docker exec "$app_container" wget -qO- http://localhost:8080/health
