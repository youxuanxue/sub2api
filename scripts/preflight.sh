#!/usr/bin/env bash
#
# preflight.sh — sub2api project wrapper.
#
# Per CLAUDE.md § 10, the dev-rules submodule template
# (`dev-rules/templates/preflight.sh`) covers all the generic checks
# (branch naming, submodule pointer, .cursor/rules drift, agent contract
# drift, story/test alignment, docs/approved discipline, approved-doc
# invariants R1-R5, doc-stat drift, cloud-agent env consistency). This
# wrapper exists ONLY because sub2api has project-specific checks that
# do not belong in the shared template:
#
#   newapi compat-pool drift     — guards the P0 regression that
#        triggered docs/approved/newapi-as-fifth-platform.md (any new
#        scheduler/gateway caller must use IsOpenAICompatPoolMember /
#        OpenAICompatPlatforms instead of bare PlatformOpenAI / IsOpenAI).
#   newapi sentinel registry     — guards the recurring upstream-merge
#        regression where load-bearing fifth-platform files / symbols get
#        silently deleted. Driven by `scripts/newapi-sentinels.json`
#        (single source of truth) via `scripts/check-newapi-sentinels.py`.
#        The same script is invoked by
#        `.github/workflows/upstream-merge-pr-shape.yml`.
#
# Usage:  ./scripts/preflight.sh [--fix]
# Exit 0 = all sections passed.  Non-zero = at least one failed.
#
set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ---- Sections 1-8: delegate to dev-rules template ----------------------------
if [ ! -x ./dev-rules/templates/preflight.sh ]; then
    echo "FAIL: dev-rules submodule not initialized."
    echo "      Run: git submodule update --init --recursive"
    exit 1
fi

PREFLIGHT_REPO_ROOT="$REPO_ROOT" ./dev-rules/templates/preflight.sh "$@"
dev_status=$?
if [ "$dev_status" -ne 0 ]; then
    exit "$dev_status"
fi

# ---- sub2api: newapi compat-pool drift --------------------------------------
# Source of truth: docs/approved/newapi-as-fifth-platform.md §5.1.
# Both checks deliberately use POSIX `grep -rnE` (not ripgrep) so they work
# in CI runners without rg installed.
echo ""
echo "=== sub2api: newapi compat-pool drift ==="
errors=0

# Check A — candidate-pool fetch must go through the TK helper
# (IsOpenAICompatPoolMember / OpenAICompatPlatforms). A new caller passing
# PlatformOpenAI directly to ListSchedulableAccounts would silently exclude
# newapi accounts and re-introduce the original P0 regression.
drift1_hits="$(grep -rnE 'ListSchedulableAccounts\([^)]*PlatformOpenAI' \
    backend/internal/service \
    --include='*.go' \
    --exclude='*_test.go' \
    --exclude='*_tk_*.go' 2>/dev/null || true)"
if [ -n "$drift1_hits" ]; then
    echo "  FAIL: direct PlatformOpenAI bucket usage outside TK helpers"
    echo "        (use OpenAICompatPlatforms / IsOpenAICompatPoolMember instead):"
    echo "$drift1_hits" | sed 's/^/    /'
    errors=$((errors + 1))
else
    echo "  ok: no direct PlatformOpenAI ListSchedulableAccounts callers outside TK helpers"
fi

# Check B — scheduler/gateway filters must not regress to bare
# `!account.IsOpenAI()`; the canonical predicate is
# `!account.IsOpenAICompatPoolMember(groupPlatform)`. The bare form silently
# rejects newapi accounts even for newapi groups.
drift2_hits="$(grep -nE '!\s*account\.IsOpenAI\(\)' \
    backend/internal/service/openai_account_scheduler.go \
    backend/internal/service/openai_gateway_service.go 2>/dev/null || true)"
if [ -n "$drift2_hits" ]; then
    echo "  FAIL: scheduling filter still uses bare !account.IsOpenAI()"
    echo "        — switch to !account.IsOpenAICompatPoolMember(groupPlatform):"
    echo "$drift2_hits" | sed 's/^/    /'
    errors=$((errors + 1))
else
    echo "  ok: scheduler / gateway filters use IsOpenAICompatPoolMember predicate"
fi

# ---- sub2api: newapi sentinel registry --------------------------------------
# Source of truth: scripts/newapi-sentinels.json. Verifies that every
# load-bearing surface of the fifth platform (`newapi`) — TK companion files,
# canonical predicates, frontend platform enumerations — is still present.
# This catches the failure mode that triggered this guard: an upstream merge
# silently dropping a file or a switch-case branch and the regression only
# surfacing weeks later in production.
echo ""
echo "=== sub2api: newapi sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read newapi-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/check-newapi-sentinels.py --quiet; then
    # check-newapi-sentinels.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all newapi sentinels intact"
fi

echo ""
if [ "$errors" -eq 0 ]; then
    echo "=== preflight (with sub2api checks): PASS ==="
    exit 0
else
    echo "=== preflight (with sub2api checks): FAIL ($errors check(s) failed) ==="
    exit 1
fi
