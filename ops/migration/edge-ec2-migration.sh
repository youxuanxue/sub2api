#!/usr/bin/env bash
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"
exec python3 "${REPO_ROOT}/ops/migration/edge_ec2_migration.py" "$@"
