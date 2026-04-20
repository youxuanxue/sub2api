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

# Future sections (TK-specific) live below; each guarded with its own header:
# § 2  contract drift            — TODO when scripts/export_agent_contract.py is ready (see docs/preflight-debt.md)
# § 3  ent schema regen guard    — TODO
# § 4  pnpm-lock sync            — TODO

echo "[preflight] OK"
