#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
artifact="$repo_root/ops/observability/generated/model-family-rules.json"

cd "$repo_root/backend"
go run ./cmd/model-family-rules --check "$artifact"
