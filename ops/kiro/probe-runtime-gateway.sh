#!/usr/bin/env bash
# Synthetic upstream-compatibility probe using the local Kiro CLI token and the
# committed CLI identity. This path never qualifies as fingerprint evidence.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
exec python3 "$SCRIPT_DIR/probe_runtime_gateway.py" "$@"
