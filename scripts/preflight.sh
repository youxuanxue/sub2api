#!/usr/bin/env bash
# Project-level preflight gate.
# Required by dev-rules/product-dev.mdc §完成自检 and dev-rules-convention.mdc §强约束门禁.
# Each section MUST exit non-zero on failure; CI and pre-commit hook will block.
#
# Install pre-commit hook (idempotent):
#   bash dev-rules/templates/install-hooks.sh
#
# Add new sections by adding `echo "[preflight] § N  ..."` headers
# so failures are easy to locate in CI logs.

set -e

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

echo "[preflight] § 1  approved-doc frontmatter / truth"
python3 scripts/check_approved_docs.py

echo "[preflight] § 2  newapi compat-pool drift"
# Source of truth: docs/approved/newapi-as-fifth-platform.md §5.1.
# Both checks are deliberately implemented with POSIX grep (not ripgrep) so
# they work in CI runners without rg installed.
#
# Drift check 2.a — candidate-pool fetch must go through the TK helper
# (IsOpenAICompatPoolMember / OpenAICompatPlatforms). A new caller passing
# PlatformOpenAI directly to ListSchedulableAccounts would silently exclude
# newapi accounts and re-introduce the §0 P0 regression.
drift1_hits="$(grep -rnE 'ListSchedulableAccounts\([^)]*PlatformOpenAI' \
    backend/internal/service \
    --include='*.go' \
    --exclude='*_test.go' \
    --exclude='*_tk_*.go' || true)"
if [[ -n "$drift1_hits" ]]; then
  echo "FAIL: direct PlatformOpenAI bucket usage outside TK helpers (use OpenAICompatPlatforms / IsOpenAICompatPoolMember instead):" >&2
  echo "$drift1_hits" >&2
  exit 1
fi
# Drift check 2.b — scheduler/gateway filters must not regress to bare
# `!account.IsOpenAI()`; the canonical predicate is
# `!account.IsOpenAICompatPoolMember(groupPlatform)`. The bare form silently
# rejects newapi accounts even for newapi groups.
drift2_hits="$(grep -nE '!\s*account\.IsOpenAI\(\)' \
    backend/internal/service/openai_account_scheduler.go \
    backend/internal/service/openai_gateway_service.go || true)"
if [[ -n "$drift2_hits" ]]; then
  echo "FAIL: scheduling filter still uses bare !account.IsOpenAI() — switch to !account.IsOpenAICompatPoolMember(groupPlatform):" >&2
  echo "$drift2_hits" >&2
  exit 1
fi
echo "[preflight] § 2  OK"

echo "[preflight] § 3  agent contract notes coverage"
# Source of truth: docs/agent_integration.md `# Agent Contract Notes` tail.
# Hard-fails only on platform-coverage gaps (the §0-grade regression we
# are guarding); route-count drift is reported as a soft warning until
# the prefix-resolving generator lands (see docs/preflight-debt.md).
python3 scripts/export_agent_contract.py --check
echo "[preflight] § 3  OK"

# Future sections (TK-specific) live below; each guarded with its own header:
# § 4  ent schema regen guard    — TODO
# § 5  pnpm-lock sync            — TODO

echo "[preflight] OK"
