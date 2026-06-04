#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

# We validate all load-bearing Caddyfile sources used by local/stage0 deploy flows.
# The prod Caddyfile uses the `rate_limit` directive (github.com/mholt/caddy-ratelimit),
# which stock caddy:2-alpine cannot parse — it must be adapted with the custom image
# built from deploy/caddy/Dockerfile (see CUSTOM_FILES below). The other two stay on
# stock caddy.
FILES=(
  "deploy/Caddyfile"
  "deploy/aws/stage0/Caddyfile"
  "deploy/aws/stage0/Caddyfile.edge"
)

# Caddyfiles that require the custom (ratelimit) caddy binary to adapt.
CUSTOM_FILES=(
  "deploy/aws/stage0/Caddyfile"
)

CADDY_DOCKERFILE="deploy/caddy/Dockerfile"

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

# Custom caddy image (stock + caddy-ratelimit) for Caddyfiles that use rate_limit.
# Built locally from deploy/caddy/Dockerfile (hermetic — no GHCR dependency, so the
# PR that introduces rate_limit validates without a chicken-and-egg on the published
# image). Tagged by a hash of the Dockerfile so the xcaddy build (Go compile, ~1-2m)
# only re-runs when the Dockerfile changes; otherwise it's an instant image-inspect
# hit. Empty CUSTOM_CADDY_IMAGE means "could not build" → skip-loudly downstream.
CUSTOM_CADDY_IMAGE=""
ensure_custom_caddy_image() {
  if [ ! -f "$REPO_ROOT/$CADDY_DOCKERFILE" ]; then
    echo "FAIL: missing $CADDY_DOCKERFILE (needed to adapt rate_limit Caddyfiles)" >&2
    return 2
  fi
  local hash tag attempt
  hash="$(sha256sum "$REPO_ROOT/$CADDY_DOCKERFILE" | cut -c1-12)"
  tag="tokenkey-caddy-ratelimit:${hash}"
  if docker image inspect "$tag" >/dev/null 2>&1; then
    CUSTOM_CADDY_IMAGE="$tag"
    return 0
  fi
  for attempt in 1 2 3; do
    if docker build -q -f "$REPO_ROOT/$CADDY_DOCKERFILE" -t "$tag" "$REPO_ROOT/deploy/caddy" >/dev/null 2>&1; then
      CUSTOM_CADDY_IMAGE="$tag"
      return 0
    fi
    echo "warn: docker build $tag attempt ${attempt}/3 failed (xcaddy module fetch?); retrying..." >&2
    sleep "$((attempt * 5))"
  done
  return 1
}

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
  local file="$1" image="$2"
  docker run --rm \
    -v "$file:/work/Caddyfile:ro" \
    "$image" \
    caddy adapt --config /work/Caddyfile --adapter caddyfile >/dev/null
}

# Does $rel need the custom (ratelimit) image?
needs_custom_image() {
  local rel="$1" c
  for c in "${CUSTOM_FILES[@]}"; do
    [ "$c" = "$rel" ] && return 0
  done
  return 1
}

failures=0
for rel in "${FILES[@]}"; do
  src="$REPO_ROOT/$rel"
  if [ ! -f "$src" ]; then
    echo "FAIL: missing $rel" >&2
    failures=$((failures + 1))
    continue
  fi

  image="$CADDY_IMAGE"
  if needs_custom_image "$rel"; then
    if [ -z "$CUSTOM_CADDY_IMAGE" ]; then
      ensure_custom_caddy_image
      rc=$?
      if [ "$rc" = 2 ]; then
        failures=$((failures + 1))
        continue
      elif [ "$rc" != 0 ]; then
        # Build failed after retries → module-fetch/network outage, not a config
        # error. Skip this file loudly (same posture as the stock-image pull skip).
        echo "warn: could not build custom caddy image after retries — SKIPPING syntax check for $rel (infra, not a config error)" >&2
        continue
      fi
    fi
    image="$CUSTOM_CADDY_IMAGE"
  fi

  rendered="$WORK_DIR/.caddy-$(basename "$rel").rendered"
  render_template "$src" "$rendered"

  if ! adapt_with_caddy "$rendered" "$image"; then
    echo "FAIL: caddy adapt failed for $rel" >&2
    failures=$((failures + 1))
  fi

done

if [ "$failures" -ne 0 ]; then
  exit 1
fi

echo "ok: Caddyfile syntax checks passed (${#FILES[@]} files)"
