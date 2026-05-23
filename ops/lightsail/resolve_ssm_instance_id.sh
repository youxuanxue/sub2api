#!/usr/bin/env bash
# Resolve Lightsail Edge SSM managed instance id from Parameter Store.
set -euo pipefail

SSM_PREFIX="${1:-${SSM_PREFIX:-}}"
REGION="${2:-${AWS_REGION:-}}"

if [[ -z "$SSM_PREFIX" || -z "$REGION" ]]; then
  echo "resolve_ssm_instance_id: SSM_PREFIX and REGION required" >&2
  exit 1
fi

param_name="${SSM_PREFIX}/ssm_managed_instance_id"
id="$(aws ssm get-parameter --region "$REGION" --name "$param_name" --query 'Parameter.Value' --output text 2>/dev/null || true)"
if [[ -z "$id" || "$id" == "None" ]]; then
  echo "::error::missing ${param_name}; run provision first" >&2
  exit 1
fi
echo "$id"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "managed_instance_id=${id}" >>"$GITHUB_OUTPUT"
fi
