#!/usr/bin/env bash
#
# scripts/pre-push-web-surface.sh — lightweight pre-push gate
#
# Runs ONLY the web surface alignment check before push, independent of the
# full preflight. This catches missing `no-web-impact` declarations before
# CI, avoiding wasted CI cycles.
#
# Install:  ln -sf ../../scripts/pre-push-web-surface.sh .git/hooks/pre-push
# Or:       bash dev-rules/templates/install-hooks.sh  (if updated to install pre-push)
#
# Why separate from pre-commit?
# The pre-commit hook runs the full preflight which may fail for unrelated
# reasons (branch naming, env vars, etc.), causing developers to --no-verify
# and bypass ALL checks including this one. A focused pre-push hook survives
# that bypass because pre-push is a separate hook.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

PYTHON_BIN="${PYTHON_BIN:-$(command -v python3 2>/dev/null || command -v python 2>/dev/null || echo python3)}"
CHECK_SCRIPT="dev-rules/scripts/check_web_surface_alignment.py"

if [ ! -f "$CHECK_SCRIPT" ]; then
    exit 0
fi

if ! "$PYTHON_BIN" "$CHECK_SCRIPT" --base "${PREFLIGHT_BASE:-origin/main}" 2>&1; then
    echo ""
    echo "pre-push: web surface alignment check failed."
    echo "If this is a backend-only change, add 'no-web-impact' to a commit message."
    echo "Bypass with: git push --no-verify (discouraged)"
    exit 1
fi
