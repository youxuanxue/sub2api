#!/usr/bin/env bash
#
# .cursor/cloud-agent-install.sh — stable cloud bootstrap entrypoint.
#
# Why this wrapper exists:
# - `.cursor/environment.json` is executed before submodules are guaranteed
#   to be initialized in a fresh cloud session.
# - We now use `dev-rules/templates/cloud-agent-bootstrap.sh` as the real
#   installer, but that path lives inside the `dev-rules` submodule.
# - If the submodule is absent, pointing environment.json directly at the
#   template fails with:
#     "bash: dev-rules/templates/cloud-agent-bootstrap.sh: No such file or directory"
#
# This wrapper keeps backward compatibility and guarantees the template exists
# before delegating.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

echo "[cloud-agent-install] ensuring dev-rules submodule is initialized"
git submodule update --init --recursive dev-rules

BOOTSTRAP="dev-rules/templates/cloud-agent-bootstrap.sh"
if [ ! -f "$BOOTSTRAP" ]; then
  echo "[cloud-agent-install] ERROR: missing bootstrap template: $BOOTSTRAP" >&2
  exit 1
fi

echo "[cloud-agent-install] delegating to $BOOTSTRAP"
exec bash "$BOOTSTRAP" "$@"
