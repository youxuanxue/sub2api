#!/usr/bin/env bash
# Deliver via run-probe.sh; the registered contract ships the Python body and the
# Tencent prod matrix (identity SSOT) next to this wrapper, and run-probe always
# ships ops/lib/resolve_app_container.py. Plan by default; APPLY is explicit.
set -euo pipefail
exec python3 "$(dirname "$0")/retire_dormant_media_storage.py"
