#!/usr/bin/env bash
# Edge OAuth pool + prod mirror stub onboard wrapper.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "$SCRIPT_DIR/edge_prod_relay_onboard.py" "$@"
