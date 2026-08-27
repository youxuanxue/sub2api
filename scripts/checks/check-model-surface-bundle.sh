#!/usr/bin/env bash
set -euo pipefail

repo_root="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
cd "$repo_root/backend"
exec go run ./cmd/account-model-mapping bundle --check ../ops/pricing/model-surface-bundle.json
