#!/usr/bin/env bash
# Advisory configuration audits belong to periodic diagnostics, not gateway rollout.
set -euo pipefail
INSTANCE_ID="${INSTANCE_ID:?INSTANCE_ID is required}"
OUT="${PROD_CONFIG_AUDIT_DIR:?PROD_CONFIG_AUDIT_DIR is required}"
mkdir -p "$OUT"
failed=0
for check in pricing exclusive-groups supplier-projection account-bindings; do
  case "$check" in
    pricing) command=(python3 ops/pricing/manage-overlay-runtime.py check) ;;
    exclusive-groups) command=(bash ops/stage0/check_exclusive_group_orphans_via_ssm.sh "$INSTANCE_ID" "daily orphan audit") ;;
    supplier-projection) command=(bash ops/observability/check-supplier-projection.sh --target prod --expected-instance-id "$INSTANCE_ID" --timeout-seconds 180) ;;
    account-bindings) command=(bash ops/observability/check-account-group-bindings.sh --target prod --expected-instance-id "$INSTANCE_ID" --timeout-seconds 180) ;;
  esac
  if "${command[@]}" >"$OUT/$check.stdout" 2>"$OUT/$check.stderr"; then
    echo "prod config audit: $check completed"
  else
    failed=1
    echo "::warning::prod config audit failed: $check (see diagnostic artifacts)"
  fi
  cat "$OUT/$check.stdout"
done
exit "$failed"
