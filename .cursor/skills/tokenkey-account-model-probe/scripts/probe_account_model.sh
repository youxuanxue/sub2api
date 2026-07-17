#!/usr/bin/env bash
# Wrapper — canonical script lives in ops/stage0/probe_account_model.sh
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
exec bash "${REPO_ROOT}/ops/stage0/probe_account_model.sh" "$@"
