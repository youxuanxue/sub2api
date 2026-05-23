#!/usr/bin/env bash
# Stage0 external health probe — used by both EC2 Edge and Lightsail Edge.
#
# Two calling shapes:
#   1) explicit URL (EC2 Edge, prod): `ops/stage0/external_health.sh https://api-uk1.tokenkey.dev`
#   2) SSM-prefix lookup (Lightsail Edge):
#        SSM_PREFIX=/tokenkey/lightsail/uk1 AWS_REGION=eu-west-2 DOMAIN=api-uk1.tokenkey.dev \
#          ops/stage0/external_health.sh
#      The script reads ${SSM_PREFIX}/public_ip and reconstructs https://${DOMAIN}.
#      The SSM-prefix branch is the convergence of the original
#      lightsail-only external_health shim into this shared Stage0 primitive
#      (review R-007 — one external_health, two callable shapes).
set -euo pipefail

API_URL="${1:-${API_URL:-}}"

if [[ -z "${API_URL}" && -n "${SSM_PREFIX:-}" && -n "${AWS_REGION:-}" ]]; then
  # preflight-allow: swallow — get-parameter exits non-zero when the parameter is
  # absent (e.g. before first Lightsail provision). We then surface a clear error
  # below instead of letting the AWS CLI stderr be the only signal.
  ip="$(aws ssm get-parameter --region "${AWS_REGION}" --name "${SSM_PREFIX}/public_ip" \
        --query 'Parameter.Value' --output text 2>/dev/null || true)"
  if [[ -n "${ip}" && "${ip}" != "None" ]]; then
    API_URL="https://${DOMAIN:-${ip}}"
  fi
fi

if [[ -z "${API_URL}" ]]; then
  echo "stage0_external_health: API_URL is required (or set SSM_PREFIX + AWS_REGION + DOMAIN)" >&2
  exit 1
fi
API_URL="${API_URL%/}"

ok=false
for i in 1 2 3; do
  response="$(curl -sS -o /dev/null -w '%{http_code} %{time_total}\n' "${API_URL}/health" || echo "000 0")"
  code="$(echo "${response}" | awk '{print $1}')"
  time_total="$(echo "${response}" | awk '{print $2}')"
  echo "try ${i}: HTTP ${code} ${time_total}s (${API_URL}/health)"
  if [[ "${code}" == "200" ]]; then
    fast="$(awk -v t="${time_total}" 'BEGIN{print (t < 5.0) ? 1 : 0}')"
    if [[ "${fast}" == "1" ]]; then
      ok=true
      break
    fi
  fi
  sleep 10
done

if [[ "${ok}" != "true" ]]; then
  echo "::error::external health check failed for ${API_URL}/health" >&2
  exit 1
fi

echo "ok: ${API_URL}/health returned 200 under 5s"
