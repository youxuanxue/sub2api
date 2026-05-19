#!/usr/bin/env bash
#
# Unit test for the pure contract-eval assembler in scripts/upstream/merge-state.sh.
# The pure assembler takes facts (PR existence, preflight result, audit result) and
# produces the contract JSON without touching git/gh, so we can exercise every
# branch of the gating logic deterministically.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HELPER="$HERE/merge-state.sh"

if [ ! -f "$HELPER" ]; then
  echo "FAIL: $HELPER missing" >&2
  exit 1
fi

pass=0
fail=0

# call_pure <pr_exists> <had_existing_pr> <any_open_pr_count> <matching_pr_count> \
#          <upstream_in_main> [<preflight_ok>] [<pr_body_audit_ok>]
call_pure() {
  STATE_FILE=/dev/null bash "$HELPER" contract-eval-pure "$@"
}

expect_field() {
  local label="$1" field="$2" expected="$3" json="$4"
  local actual
  actual="$(jq -r ".${field}" <<<"$json")"
  if [ "$actual" = "$expected" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    echo "FAIL: $label — .${field}" >&2
    echo "  expected: $expected" >&2
    echo "  actual:   $actual" >&2
    echo "  full json: $json" >&2
  fi
}

# 1) PR exists (matching) + preflight ok + audit ok → contract_ok=true, no reason_code
out="$(call_pure true false 1 1 false true true)"
expect_field "matching PR + green" contract_ok    "true" "$out"
expect_field "matching PR + green" reason_code    ""     "$out"

# 2) PR exists (matching) + preflight FAIL → PREFLIGHT_FAIL
out="$(call_pure true false 1 1 false false true)"
expect_field "preflight fail" contract_ok    "false"          "$out"
expect_field "preflight fail" reason_code    "PREFLIGHT_FAIL" "$out"

# 3) PR exists (matching) + preflight ok + audit FAIL → PR_BODY_INCOMPLETE
out="$(call_pure true false 1 1 false true false)"
expect_field "audit fail" contract_ok    "false"               "$out"
expect_field "audit fail" reason_code    "PR_BODY_INCOMPLETE"  "$out"

# 4) Preflight failure outranks audit failure when both red.
out="$(call_pure true false 1 1 false false false)"
expect_field "both red, preflight wins" reason_code "PREFLIGHT_FAIL" "$out"

# 5) had_existing_pr=true with non-matching open upstream PR → present_existing path
out="$(call_pure false true 1 0 false true true)"
expect_field "existing-PR fallback" contract_ok "true" "$out"
expect_field "existing-PR fallback" reason_code ""     "$out"

# 6) No PR but origin already contains upstream → ALREADY_SYNCED success
out="$(call_pure false false 0 0 true skip skip)"
expect_field "up_to_date" contract_ok "true"            "$out"
expect_field "up_to_date" reason_code "ALREADY_SYNCED" "$out"

# 7) No PR, origin lags upstream → CONTRACT_FAIL
out="$(call_pure false false 0 0 false skip skip)"
expect_field "missing PR" contract_ok "false"         "$out"
expect_field "missing PR" reason_code "CONTRACT_FAIL" "$out"

# 8) Backward compat: 5-arg call (no preflight/audit args) defaults to "skip"
#    and must NOT block contract_ok when PR exists. Mirrors today's caller shape.
out="$(call_pure true false 1 1 false)"
expect_field "5-arg compat: contract_ok"     contract_ok       "true" "$out"
expect_field "5-arg compat: preflight_ok"    preflight_ok      "skip" "$out"
expect_field "5-arg compat: pr_body_audit_ok" pr_body_audit_ok "skip" "$out"

# 9) "skip" behaves as a permissive sentinel: when PR exists and both gates are
#    skip, contract_ok=true. This is what the after-attempt-1 step relies on.
out="$(call_pure true false 1 1 false skip skip)"
expect_field "skip is permissive" contract_ok "true" "$out"

# 10) I/O wrapper pins gh PR lookups to GH_REPO/GITHUB_REPOSITORY so detached
#     checkouts or sibling repo operations cannot make contract eval inspect the
#     wrong repository.
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
cat > "$tmpdir/gh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" > "${GH_STUB_ARGS:?}"
printf '[]\n'
EOF
cat > "$tmpdir/git" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  fetch|remote)
    exit 0
    ;;
  merge-base)
    exit 1
    ;;
  *)
    command git "$@"
    ;;
esac
EOF
chmod +x "$tmpdir/gh" "$tmpdir/git"
GH_STUB_ARGS="$tmpdir/gh-args" GH_REPO="youxuanxue/sub2api" PATH="$tmpdir:$PATH" \
  bash "$HELPER" contract-eval-json merge/upstream-test false >/dev/null
if grep -q -- '--repo youxuanxue/sub2api' "$tmpdir/gh-args"; then
  pass=$((pass + 1))
else
  fail=$((fail + 1))
  echo "FAIL: contract_eval_json pins gh pr list to GH_REPO" >&2
  echo "  args: $(cat "$tmpdir/gh-args" 2>/dev/null || true)" >&2
fi

echo ""
if [ "$fail" -eq 0 ]; then
  echo "=== upstream-merge-state pure assembler: PASS ($pass assertions) ==="
  exit 0
else
  echo "=== upstream-merge-state pure assembler: FAIL ($fail / $((pass + fail))) ===" >&2
  exit 1
fi
