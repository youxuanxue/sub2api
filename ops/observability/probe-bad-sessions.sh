#!/usr/bin/env bash
# Read-only bad-session scan. run-probe.sh uploads the registered Python companion.
set -euo pipefail
exec python3 "$(dirname "${BASH_SOURCE[0]}")/detect-bad-sessions.py"
