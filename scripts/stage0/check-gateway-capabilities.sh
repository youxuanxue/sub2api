#!/usr/bin/env bash
# Offline post-release planning. No AWS, credentials, Docker or paid requests.
set -euo pipefail
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
output_dir="${1:?usage: check-gateway-capabilities.sh OUTPUT_DIR [PREVIOUS_PLAN]}"
mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"
cd "$repo_root"
cp "${CAPABILITY_INVENTORY:-ops/stage0/gateway-account-supply.json}" "$output_dir/inventory.json"
previous_args=()
if [[ -n "${CAPABILITY_PREVIOUS_TAG:-}" ]]; then
  python3 ops/stage0/post_release_replay_check.py from-tag --tag "$CAPABILITY_PREVIOUS_TAG" --out "$output_dir/previous-plan.json"
  if [[ -f "$output_dir/previous-plan.json" ]]; then previous_args=(--previous "$output_dir/previous-plan.json"); fi
fi
if [[ -n "${2:-}" ]]; then previous_args=(--previous "$2"); fi
python3 ops/stage0/post_release_replay_check.py plan --inventory "$output_dir/inventory.json" \
  --out "$output_dir/plan.json" ${previous_args[@]+"${previous_args[@]}"}
python3 ops/stage0/post_release_replay_check.py report --plan "$output_dir/plan.json" \
  --out "$output_dir/coverage.json" ${previous_args[@]+"${previous_args[@]}"}
