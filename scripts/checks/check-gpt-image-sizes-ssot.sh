#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
artifact="$repo_root/frontend/src/constants/gptImageSizes.generated.tk.ts"

cd "$repo_root/backend"
go run ./cmd/gpt-image-sizes-ssot --check "$artifact"
