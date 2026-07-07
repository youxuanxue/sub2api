#!/usr/bin/env bash
# release-main-push-route.sh — How to land a VERSION bump on origin/main.
#
# Prints one line to stdout (parseable):
#   direct-push  — current gh actor may push origin HEAD:main (no bump PR)
#   bump-via-pr  — use scripts/release-bump-via-pr.sh
#
# Scheme 1 routing (see scripts/release_main_push_route.py):
#   - No branch protection → direct-push
#   - Organization repo: gh user listed in bypass_pull_request_allowances.users
#   - Personal repo: enforce_admins=false and gh user has admin permission
#
# Configure once: bash scripts/release-configure-main-bypass.sh
#
# Exit 0 when route is determined; exit 2 on gh/network failure.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_RESOLVER="$SCRIPT_DIR/lib/resolve-gh-repo.sh"

if ! command -v gh >/dev/null 2>&1; then
  echo direct-push
  exit 0
fi

REPO="$(bash "$REPO_RESOLVER" "$(pwd)" 2>/dev/null)" || {
  echo "[release-main-push-route] ERROR: failed to resolve GitHub repo" >&2
  exit 2
}

PROTECTION_JSON=""
if ! PROTECTION_JSON="$(gh api "repos/$REPO/branches/main/protection" 2>/dev/null)"; then
  echo direct-push
  exit 0
fi

CURRENT_USER="$(gh api user -q .login 2>/dev/null)" || {
  echo "[release-main-push-route] ERROR: gh api user failed" >&2
  exit 2
}

META_JSON="$(gh api "repos/$REPO" --jq '{owner_type: .owner.type, admin: .permissions.admin}')"

PROTECTION_JSON="$PROTECTION_JSON" META_JSON="$META_JSON" CURRENT_USER="$CURRENT_USER" \
  python3 "$SCRIPT_DIR/release_main_push_route.py"
