#!/usr/bin/env bash
# Transport entry for run-probe.sh; Python owns target, opt-in and cleanup guards.
set -euo pipefail
exec python3 /tmp/verify_colocation_resources.py
