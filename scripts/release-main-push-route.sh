#!/usr/bin/env bash
# release-main-push-route.sh — How to land a VERSION bump on origin/main.
#
# Prints one line to stdout (parseable):
#   direct-push  — current gh actor may push origin HEAD:main (no bump PR)
#   bump-via-pr  — use scripts/release-bump-via-pr.sh
#
# Scheme 1 routing:
#   - No branch protection → direct-push
#   - Organization repo: gh user listed in bypass_pull_request_allowances.users
#   - Personal repo: enforce_admins=false and gh user has admin permission
#
# Configure once: bash scripts/release-configure-main-bypass.sh
#
# Exit 0 when route is determined; exit 2 on gh/network failure.
set -euo pipefail

if ! command -v gh >/dev/null 2>&1; then
  echo direct-push
  exit 0
fi

REPO="$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null)" || {
  echo "[release-main-push-route] ERROR: gh repo view failed" >&2
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

if PROTECTION_JSON="$PROTECTION_JSON" META_JSON="$META_JSON" CURRENT_USER="$CURRENT_USER" python3 - <<'PY'
import json, os, sys

prot = json.loads(os.environ["PROTECTION_JSON"])
meta = json.loads(os.environ["META_JSON"])
current = os.environ["CURRENT_USER"]

if meta.get("owner_type") == "Organization":
    reviews = prot.get("required_pull_request_reviews") or {}
    allow = reviews.get("bypass_pull_request_allowances") or {}
    logins = []
    for item in allow.get("users") or []:
        if isinstance(item, dict) and item.get("login"):
            logins.append(item["login"])
        elif isinstance(item, str):
            logins.append(item)
    if current in logins:
        sys.exit(0)
    sys.exit(1)

# Personal repo: admins bypass when enforce_admins is disabled.
enforce_admins = bool((prot.get("enforce_admins") or {}).get("enabled"))
if not enforce_admins and meta.get("admin"):
    sys.exit(0)
sys.exit(1)
PY
then
  echo direct-push
else
  echo bump-via-pr
fi
