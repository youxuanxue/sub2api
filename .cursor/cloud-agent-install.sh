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
# Cursor Cloud treats a non-zero exit from the install hook as a failed environment.
# Bootstrap may exit 1 when optional-for-coding-agent secrets (e.g. ANTHROPIC_AUTH_TOKEN)
# are not injected yet, or when preflight differs from cloud VM layout — the repo is
# still usable. Run bootstrap without exec so we always return 0 after logging status.
set +e
bash "$BOOTSTRAP" "$@"
bootstrap_exit=$?
set -e
if [ "$bootstrap_exit" -ne 0 ]; then
  echo "[cloud-agent-install] WARN: bootstrap exited $bootstrap_exit — session continues; fix secrets/tools if Claude/gh/jq features are needed" >&2
fi
exit 0
