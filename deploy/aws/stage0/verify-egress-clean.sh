#!/usr/bin/env bash
# Probe the edge's outbound IP for upstream-API pollution by running curl ON
# THE EDGE ITSELF via SSM SendCommand.
#
# Replaces the throwaway t4g.nano pattern from the prior manual rotation
# runbook: now that operation=rotate_egress_ip in deploy-edge-stage0.yml does
# a CFN-native swap (which keeps the edge instance and its SSM agent in place),
# the edge is already a perfectly source-IP-controlled probe. One fewer
# ephemeral resource, one fewer IAM scope, one fewer thing that can leak.
#
# Probe signal (per docs/deploy/tokenkey-edge-ip-history.md § 4 step 4):
#   * clean → Anthropic returns 401/400 with provider-shaped JSON
#   * polluted → 403 with Cloudflare challenge HTML in the body
#
# Usage:
#   verify-egress-clean.sh <instance-id> <region> <expected-public-ip>
#
# Exit codes:
#   0  — clean (or only soft-fail signals; see output for per-upstream verdicts)
#   1  — polluted (at least one upstream returned 403 + Cloudflare HTML)
#   2  — bad input, aws CLI failure, SendCommand never returned, or outbound
#        IP did not match expected (the swap may not have propagated yet)

set -euo pipefail

INSTANCE_ID="${1:-}"
REGION="${2:-}"
EXPECTED_IP="${3:-}"

if [ -z "$INSTANCE_ID" ] || [ -z "$REGION" ] || [ -z "$EXPECTED_IP" ]; then
  echo "usage: $0 <instance-id> <region> <expected-public-ip>" >&2
  exit 2
fi

if ! command -v aws >/dev/null 2>&1; then
  echo "aws CLI not found" >&2
  exit 2
fi

# The probe script we ask the edge to run. Kept short on purpose: SSM
# SendCommand --parameters JSON escaping is brittle with long multi-line bash.
# Output line shape (one line per upstream):
#   PROBE upstream=anthropic http_code=401 cf_challenge=0
#   PROBE upstream=openai    http_code=401 cf_challenge=0
#   PROBE upstream=google    http_code=400 cf_challenge=0
#   ACTUAL_IP=<x.y.z.w>
PROBE_SCRIPT=$(cat <<'PROBESH'
set -u
ACTUAL_IP="$(curl -sS --max-time 10 https://api.ipify.org || echo unknown)"
echo "ACTUAL_IP=${ACTUAL_IP}"

probe() {
  local name="$1" url="$2" hdr="$3" body="$4"
  local body_file http_code cf
  body_file="$(mktemp)"
  http_code="$(curl -sS --max-time 15 -o "${body_file}" -w '%{http_code}' \
    -H "${hdr}" \
    -X POST -H 'content-type: application/json' \
    --data "${body}" \
    "${url}" 2>/dev/null || echo 000)"
  cf=0
  if [ "${http_code}" = "403" ] && grep -qiE 'cloudflare|attention required|cf-(ray|bm)|challenge' "${body_file}" 2>/dev/null; then
    cf=1
  fi
  echo "PROBE upstream=${name} http_code=${http_code} cf_challenge=${cf}"
  rm -f "${body_file}"
}

probe anthropic 'https://api.anthropic.com/v1/messages' 'x-api-key: dummy_for_probe' '{"model":"claude-sonnet-4-6","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}'
probe openai    'https://api.openai.com/v1/chat/completions' 'authorization: Bearer dummy_for_probe' '{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}'

# Google takes the key as a query parameter; use a HEAD-equivalent GET so the
# probe shape is identical even though the request is read-only.
body_file="$(mktemp)"
http_code="$(curl -sS --max-time 15 -o "${body_file}" -w '%{http_code}' \
  'https://generativelanguage.googleapis.com/v1beta/models?key=dummy_for_probe' 2>/dev/null || echo 000)"
cf=0
if [ "${http_code}" = "403" ] && grep -qiE 'cloudflare|attention required|cf-(ray|bm)|challenge' "${body_file}" 2>/dev/null; then
  cf=1
fi
echo "PROBE upstream=google http_code=${http_code} cf_challenge=${cf}"
rm -f "${body_file}"
PROBESH
)

PROBE_B64=$(printf '%s' "$PROBE_SCRIPT" | base64 | tr -d '\n')
RUNLINE="printf '%s' '${PROBE_B64}' | base64 -d | bash"

CMD_ID=$(aws ssm send-command --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --parameters "{\"commands\":[\"${RUNLINE}\"]}" \
  --comment "tokenkey rotate_egress_ip pollution probe" \
  --query 'Command.CommandId' --output text 2>&1) || {
    echo "::error::verify-egress-clean: ssm send-command failed: ${CMD_ID}" >&2
    exit 2
  }

# Poll up to 60s for the command to complete.
deadline=$(( $(date +%s) + 60 ))
inv_status=""
while [ "$(date +%s)" -lt "$deadline" ]; do
  inv_status=$(aws ssm get-command-invocation --region "$REGION" \
    --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
    --query 'Status' --output text 2>/dev/null || echo Pending)
  case "$inv_status" in
    Success|Failed|Cancelled|TimedOut|Cancelling) break ;;
  esac
  sleep 3
done

if [ "$inv_status" != "Success" ]; then
  echo "::error::verify-egress-clean: SSM invocation did not Succeed (status=${inv_status:-unknown}, command-id=${CMD_ID})" >&2
  exit 2
fi

stdout=$(aws ssm get-command-invocation --region "$REGION" \
  --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
  --query 'StandardOutputContent' --output text)

echo "$stdout"

actual_ip=$(printf '%s\n' "$stdout" | awk -F= '/^ACTUAL_IP=/ {print $2; exit}')
if [ -z "$actual_ip" ] || [ "$actual_ip" = "unknown" ]; then
  echo "::error::verify-egress-clean: edge could not determine its own public IP (curl ipify failed)" >&2
  exit 2
fi
if [ "$actual_ip" != "$EXPECTED_IP" ]; then
  echo "::error::verify-egress-clean: outbound IP mismatch — edge reports ${actual_ip}, rotation expected ${EXPECTED_IP}. The EIP swap may not have propagated yet, or the EIPAssociation never landed." >&2
  exit 2
fi

polluted_lines=$(printf '%s\n' "$stdout" | awk '/^PROBE / && /cf_challenge=1/ {print}' || true)
if [ -n "$polluted_lines" ]; then
  echo "::error::verify-egress-clean: outbound IP ${actual_ip} is polluted at one or more upstreams:" >&2
  printf '%s\n' "$polluted_lines" >&2
  exit 1
fi

echo "verify-egress-clean: ${actual_ip} is clean across Anthropic / OpenAI / Google probes"
exit 0
