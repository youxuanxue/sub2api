#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

# We validate all load-bearing Caddyfile sources used by local/stage0 deploy flows.
FILES=(
  "deploy/Caddyfile"
  "deploy/aws/stage0/Caddyfile"
  "deploy/aws/stage0/Caddyfile.edge"
)

if ! command -v docker >/dev/null 2>&1; then
  if [ -n "${CI:-}" ]; then
    echo "FAIL: docker not on PATH (required for Caddyfile syntax check)" >&2
    exit 1
  fi
  echo "skip: docker not on PATH; Caddyfile syntax check is enforced in CI" >&2
  exit 0
fi

if ! docker info >/dev/null 2>&1; then
  if [ -n "${CI:-}" ]; then
    echo "FAIL: docker daemon unavailable in CI (required for Caddyfile syntax check)" >&2
    exit 1
  fi
  echo "skip: docker daemon unavailable locally; Caddyfile syntax check is enforced in CI" >&2
  exit 0
fi

# Use deterministic placeholder values for stage0 templates that are rendered via envsubst in Cloud-Init.
export API_DOMAIN="api.example.com"
export ACME_EMAIL="ops@example.com"
export MAIN_GATEWAY_ALLOWED_CIDR="203.0.113.10/32"

WORK_DIR="${CLAUDE_JOB_DIR:-$REPO_ROOT/.claude/tmp}"
mkdir -p "$WORK_DIR"

render_template() {
  local src="$1"
  local out="$2"
  # Keep replacement intentionally narrow to avoid surprising transformations.
  sed \
    -e "s|\${API_DOMAIN}|$API_DOMAIN|g" \
    -e "s|\${ACME_EMAIL}|$ACME_EMAIL|g" \
    -e "s|\${MAIN_GATEWAY_ALLOWED_CIDR}|$MAIN_GATEWAY_ALLOWED_CIDR|g" \
    "$src" > "$out"
}

adapt_with_caddy() {
  local file="$1"
  docker run --rm \
    -v "$file:/work/Caddyfile:ro" \
    caddy:2-alpine \
    caddy adapt --config /work/Caddyfile --adapter caddyfile >/dev/null
}

failures=0
for rel in "${FILES[@]}"; do
  src="$REPO_ROOT/$rel"
  if [ ! -f "$src" ]; then
    echo "FAIL: missing $rel" >&2
    failures=$((failures + 1))
    continue
  fi

  rendered="$WORK_DIR/.caddy-$(basename "$rel").rendered"
  render_template "$src" "$rendered"

  if ! adapt_with_caddy "$rendered"; then
    echo "FAIL: caddy adapt failed for $rel" >&2
    failures=$((failures + 1))
  fi

done

if [ "$failures" -ne 0 ]; then
  exit 1
fi

echo "ok: Caddyfile syntax checks passed (${#FILES[@]} files)"
