#!/usr/bin/env bash
# Prod exclusive-group orphan check via run-probe (TAT default).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMMENT="${1:-stage0 exclusive-group orphan check}"
TARGET="${EXCLUSIVE_GROUP_PROBE_TARGET:-prod}"

stdout_file="$(mktemp)"
stderr_file="$(mktemp)"
trap 'rm -f "$stdout_file" "$stderr_file"' EXIT

set +e
bash "$ROOT/ops/observability/run-probe.sh" \
  --target "$TARGET" \
  --via tat \
  --node app-old \
  --with "$ROOT/ops/stage0/exclusive_group_orphans.sql" \
  --script "$ROOT/ops/stage0/probe-exclusive-group-orphans.sh" \
  --comment "$COMMENT" \
  --timeout-seconds "${SSM_TIMEOUT_SECONDS:-120}" \
  >"$stdout_file" 2>"$stderr_file"
probe_rc=$?
set -e

if [ "$probe_rc" -ne 0 ]; then
  detail="$(tr '\n' ' ' <"$stderr_file" | cut -c1-400)"
  echo "::warning::exclusive-group orphan check: transport failed (rc=${probe_rc}); skipped — NOT a deploy blocker. ${detail}"
  exit 0
fi

count_line="$(grep -E '^orphans=' "$stdout_file" | head -1 || true)"
orphans="${count_line#orphans=}"
if [[ -z "${orphans}" || ! "${orphans}" =~ ^[0-9]+$ ]]; then
  echo "::warning::exclusive-group orphan check: could not parse orphan count from psql output; skipped — NOT a deploy blocker."
  exit 0
fi

dormant_line="$(grep -E '^dormant_orphans=[0-9]+$' "$stdout_file" | head -1 || true)"
[[ -z "$dormant_line" ]] || echo "exclusive-group orphan inventory: $dormant_line (inactive user/key/group; no active-access finding)"

if [[ "${orphans}" -eq 0 ]]; then
  echo "exclusive-group orphan check: ok (orphans=0)"
  exit 0
fi

echo "::warning::exclusive-group orphan check: ${orphans} api key(s) bound to an exclusive standard group whose user is NOT in user_allowed_groups — they will 403 GROUP_NOT_ALLOWED. Confirm intended-revocation vs backfill the grant (remedy SQL in script header). Affected:"
{ grep -E '^orphan_detail\|' "$stdout_file" || true; } | while IFS='|' read -r _tag key_id user_id email group_id group_name; do
  echo "::warning::  api_key=${key_id} user=${user_id} <${email}> group=${group_id} (${group_name})"
done
exit 0