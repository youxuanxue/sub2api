#!/usr/bin/env bash
# External health for Lightsail Edge using stored public IP / domain.
set -euo pipefail

API_URL="${1:-${API_URL:-}}"
if [[ -z "$API_URL" ]]; then
  SSM_PREFIX="${SSM_PREFIX:-}"
  REGION="${AWS_REGION:-}"
  DOMAIN="${DOMAIN:-}"
  if [[ -n "$SSM_PREFIX" && -n "$REGION" ]]; then
    ip="$(aws ssm get-parameter --region "$REGION" --name "${SSM_PREFIX}/public_ip" \
      --query 'Parameter.Value' --output text 2>/dev/null || true)"
    if [[ -n "$ip" && "$ip" != "None" ]]; then
      API_URL="https://${DOMAIN:-${ip}}"
    fi
  fi
fi
if [[ -z "$API_URL" ]]; then
  echo "lightsail external health: API_URL or SSM_PREFIX+DOMAIN required" >&2
  exit 1
fi
exec bash "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/stage0/external_health.sh" "$API_URL"
