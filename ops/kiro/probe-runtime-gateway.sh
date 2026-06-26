#!/usr/bin/env bash
# Probe Kiro runtime.us-east-1.kiro.dev / management.us-east-1.kiro.dev with the
# local IDE OAuth token — no mitm, no system-proxy surgery.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
exec python3 "$SCRIPT_DIR/probe_runtime_gateway.py" "$@"
