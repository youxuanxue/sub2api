#!/usr/bin/env bash
#
# preflight.sh — sub2api project wrapper.
#
# Per CLAUDE.md § 10, the dev-rules submodule template
# (`dev-rules/templates/preflight.sh`, 8 sections) covers everything
# generic. This wrapper exists ONLY because sub2api has one project-
# specific check that does not belong in the shared template:
#
#   § 9  newapi compat-pool drift  — guards the P0 regression that
#        triggered docs/approved/newapi-as-fifth-platform.md (any new
#        scheduler/gateway caller must use IsOpenAICompatPoolMember /
#        OpenAICompatPlatforms instead of bare PlatformOpenAI / IsOpenAI).
#
# Sections 1-8 (branch naming, submodule pointer, .cursor/rules drift,
# agent contract drift, story/test alignment, docs/approved discipline,
# approved-doc invariants R1-R5, doc-stat drift) are NOT duplicated
# here — they are run by delegating to the dev-rules template.
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

# ---- § 9: sub2api-specific newapi compat-pool drift -------------------------
# Source of truth: docs/approved/newapi-as-fifth-platform.md §5.1.
# Both checks deliberately use POSIX `grep -rnE` (not ripgrep) so they work
# in CI runners without rg installed.
echo ""
echo "=== § 9  sub2api: newapi compat-pool drift ==="
errors=0

# 9.a — candidate-pool fetch must go through the TK helper
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

# 9.b — scheduler/gateway filters must not regress to bare
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

echo ""
if [ "$errors" -eq 0 ]; then
    echo "=== § 10 sub2api: automated PR backlog guard ==="
    BRANCH="$(git rev-parse --abbrev-ref HEAD)"
    case "$BRANCH" in
        fix/*|merge/upstream-*|hotfix/*)
            echo "  ok: branch $BRANCH bypasses automated PR backlog guard"
            ;;
        *)
            PENDING_AUTO="$(gh pr list --label automated --state open --json number 2>/dev/null | jq length)"
            if [ "${PENDING_AUTO:-0}" -gt 5 ]; then
                echo "::error::已有 $PENDING_AUTO 个 auto PR 未处理 (上限 5),先消化再提交新 feature/chore/docs 代码"
                echo "       fix/* 分支可 bypass; 紧急情况 git commit --no-verify (但 CI 仍会 fail)"
                exit 1
            fi
            echo "  ok: pending automated PRs = ${PENDING_AUTO:-0}"
            ;;
    esac

    echo ""
    echo "=== § 11 sub2api: open P1 issue warning ==="
    P1_OPEN="$(gh issue list --label p1 --state open --json number 2>/dev/null | jq length)"
    if [ "${P1_OPEN:-0}" -gt 0 ]; then
        echo "::warning::有 $P1_OPEN 个 P1 issue 未处理(不阻断 commit,但请优先处理)"
    else
        echo "  ok: no open P1 issues"
    fi

    echo ""
    echo "=== preflight (with § 9-11 sub2api): PASS ==="
    exit 0
else
    echo "=== preflight (with § 9 sub2api): FAIL ($errors check(s) failed in § 9) ==="
    exit 1
fi
