#!/usr/bin/env bash
set -euo pipefail

API_URL="${1:-${API_URL:-}}"
if [[ -z "${API_URL}" ]]; then
  echo "stage0_external_health: API_URL is required" >&2
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
