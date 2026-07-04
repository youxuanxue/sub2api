#!/usr/bin/env bash
# release-configure-main-bypass.sh — Enable direct-push VERSION bumps on protected main
# (scheme 1).
#
# Personal repos (User owner): GitHub does not support bypass_pull_request_allowances
# users — we set enforce_admins=false so repository admins can bypass PR rules.
#
# Organization repos: merge release actors into bypass_pull_request_allowances.users.
#
# Usage:
#   bash scripts/release-configure-main-bypass.sh              # configure for current gh user
#   bash scripts/release-configure-main-bypass.sh --check      # verify direct-push route
#   TK_RELEASE_BYPASS_USERS=u1,u2 bash scripts/release-configure-main-bypass.sh  # org only
#
# Requires: gh authenticated with admin rights on the repo.
set -euo pipefail

CHECK_ONLY=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --check) CHECK_ONLY=1; shift ;;
    -h|--help)
      sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "[release-configure-main-bypass] ERROR: unknown arg: $1" >&2; exit 1 ;;
  esac
done

if ! command -v gh >/dev/null 2>&1; then
  echo "[release-configure-main-bypass] ERROR: gh required" >&2
  exit 2
fi

REPO="$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null)" || {
  echo "[release-configure-main-bypass] ERROR: gh repo view failed" >&2
  exit 2
}

if ! gh api "repos/$REPO/branches/main/protection" >/dev/null 2>&1; then
  echo "[release-configure-main-bypass] main has no branch protection; direct-push already works"
  exit 0
fi

if [ "$CHECK_ONLY" -eq 1 ]; then
  route="$(bash "$(dirname "$0")/release-main-push-route.sh")"
  if [ "$route" = "direct-push" ]; then
    echo "[release-configure-main-bypass] ok: release-main-push-route=$route"
    exit 0
  fi
  echo "[release-configure-main-bypass] FAIL: release-main-push-route=$route (expected direct-push)" >&2
  exit 1
fi

META_JSON="$(gh api "repos/$REPO" --jq '{owner_type: .owner.type, owner_login: .owner.login}')"
PROT_JSON="$(gh api "repos/$REPO/branches/main/protection")"
REQUESTED_CSV="${TK_RELEASE_BYPASS_USERS:-$(gh api user -q .login)}"

RESULT="$(META_JSON="$META_JSON" PROT_JSON="$PROT_JSON" REQUESTED_CSV="$REQUESTED_CSV" python3 - <<'PY'
import json, os, sys

meta = json.loads(os.environ["META_JSON"])
prot = json.loads(os.environ["PROT_JSON"])
requested = [u.strip() for u in os.environ.get("REQUESTED_CSV", "").split(",") if u.strip()]
is_org = meta.get("owner_type") == "Organization"

reviews = prot.get("required_pull_request_reviews") or {}
allow = reviews.get("bypass_pull_request_allowances") or {}
existing = []
for item in allow.get("users") or []:
    if isinstance(item, dict) and item.get("login"):
        existing.append(item["login"])
    elif isinstance(item, str):
        existing.append(item)

merged = []
seen = set()
for u in existing + requested:
    if u and u not in seen:
        seen.add(u)
        merged.append(u)

enforce_admins = bool((prot.get("enforce_admins") or {}).get("enabled"))
mode = "org-bypass-users" if is_org else "personal-enforce-admins-off"
if not is_org:
    enforce_admins = False

checks = (prot.get("required_status_checks") or {}).get("checks") or []
if not checks:
    for ctx in (prot.get("required_status_checks") or {}).get("contexts") or []:
        checks.append({"context": ctx, "app_id": None})

pr_reviews = {
    "dismiss_stale_reviews": bool(reviews.get("dismiss_stale_reviews")),
    "require_code_owner_reviews": bool(reviews.get("require_code_owner_reviews")),
    "require_last_push_approval": bool(reviews.get("require_last_push_approval")),
    "required_approving_review_count": int(reviews.get("required_approving_review_count") or 0),
}
if is_org:
    pr_reviews["bypass_pull_request_allowances"] = {
        "users": merged,
        "teams": [],
        "apps": [],
    }

payload = {
    "required_status_checks": {
        "strict": bool((prot.get("required_status_checks") or {}).get("strict")),
        "checks": [{"context": c["context"], "app_id": c.get("app_id")} for c in checks],
    },
    "enforce_admins": enforce_admins,
    "required_pull_request_reviews": pr_reviews,
    "restrictions": None,
    "required_linear_history": bool((prot.get("required_linear_history") or {}).get("enabled")),
    "allow_force_pushes": bool((prot.get("allow_force_pushes") or {}).get("enabled")),
    "allow_deletions": bool((prot.get("allow_deletions") or {}).get("enabled")),
    "block_creations": bool((prot.get("block_creations") or {}).get("enabled")),
    "required_conversation_resolution": bool(
        (prot.get("required_conversation_resolution") or {}).get("enabled")
    ),
    "lock_branch": bool((prot.get("lock_branch") or {}).get("enabled")),
    "allow_fork_syncing": bool((prot.get("allow_fork_syncing") or {}).get("enabled")),
}
detail = merged if is_org else ["admin-bypass-via-enforce_admins=false"]
print(f"apply\t{mode}\t{','.join(detail)}\t{json.dumps(payload)}")
PY
)"

IFS=$'\t' read -r _ mode detail payload <<< "$RESULT"
echo "[release-configure-main-bypass] mode=$mode actors=$detail"
printf '%s\n' "$payload" | gh api -X PUT "repos/$REPO/branches/main/protection" --input - >/dev/null
echo "[release-configure-main-bypass] done; verify: bash scripts/release-main-push-route.sh"
