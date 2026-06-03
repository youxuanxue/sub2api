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

CADDY_IMAGE="caddy:2-alpine"

# Ensure the caddy image is on disk BEFORE the per-file `docker run`, with retry.
# Otherwise every `docker run` re-pulls from Docker Hub, and a transient registry
# timeout (registry-1.docker.io context-deadline) is misreported as "caddy adapt
# failed" — i.e. a CI flake, not a Caddyfile error. Returns: 0 image ready,
# 1 image genuinely unreachable after retries.
ensure_caddy_image() {
  if docker image inspect "$CADDY_IMAGE" >/dev/null 2>&1; then
    return 0
  fi
  local attempt
  for attempt in 1 2 3; do
    if docker pull "$CADDY_IMAGE" >/dev/null 2>&1; then
      return 0
    fi
    echo "warn: docker pull $CADDY_IMAGE attempt ${attempt}/3 failed; retrying..." >&2
    sleep "$((attempt * 5))"
  done
  return 1
}

if ! ensure_caddy_image; then
  # Could not fetch the image after retries → registry/network outage, not a
  # config error. Do NOT fail the gate on Docker Hub being down (that is the
  # flake this guard exists to avoid). Skip loudly; a real Caddyfile error during
  # an outage is still caught by the local pre-commit run (image usually cached).
  echo "warn: could not pull $CADDY_IMAGE after retries (registry/network issue) — SKIPPING Caddyfile syntax check (infra, not a config error)" >&2
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
    "$CADDY_IMAGE" \
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
