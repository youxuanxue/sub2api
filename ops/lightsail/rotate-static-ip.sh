#!/usr/bin/env bash
# Lightsail Edge IP rotation primitive.
#
# Mirrors the intent of the EC2 EIP-rotation skill (tokenkey-stage0-edge-ip-
# rotation) — swap to a fresh public IP when the live one is risk-blocked /
# "polluted" — but uses Lightsail Static IP APIs instead of EC2 EIP. Three-step
# swap with explicit verification gates:
#
#   1) allocate a new Static IP under <static_ip_name>-rotated-<ts>
#   2) detach the old Static IP from the Lightsail instance, attach the new one
#   3) release the old Static IP only after the new attach is confirmed
#
# DNS is intentionally NOT touched here — the operator updates Porkbun once
# the new IP is verified clean. Same posture as the EC2 EIP rotation skill:
# rotation primitive owns AWS state; DNS swap is the human gate.
#
# Usage:
#   bash ops/lightsail/rotate-static-ip.sh <edge_id> [--apply]
# Without --apply the script only prints the plan (no AWS mutation).

set -euo pipefail

EDGE_ID="${1:-}"
APPLY="${2:-}"
if [[ -z "$EDGE_ID" ]]; then
  echo "usage: $0 <edge_id> [--apply]" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && cd .. && pwd)"
MATRIX="${REPO_ROOT}/deploy/aws/lightsail/edge-targets-lightsail.json"
if [[ ! -f "$MATRIX" ]]; then
  echo "lightsail matrix not found: $MATRIX" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required for ops/lightsail/rotate-static-ip.sh" >&2
  exit 1
fi

region="$(jq -r --arg id "$EDGE_ID" '.targets[$id].lightsail_region // empty' "$MATRIX")"
instance_name="$(jq -r --arg id "$EDGE_ID" '.targets[$id].instance_name // empty' "$MATRIX")"
static_ip_name="$(jq -r --arg id "$EDGE_ID" '.targets[$id].static_ip_name // empty' "$MATRIX")"
domain="$(jq -r --arg id "$EDGE_ID" '.targets[$id].domain // empty' "$MATRIX")"
ssm_prefix="$(jq -r --arg id "$EDGE_ID" '.targets[$id].ssm_prefix // empty' "$MATRIX")"

if [[ -z "$region" || -z "$instance_name" || -z "$static_ip_name" || -z "$domain" ]]; then
  echo "edge_id ${EDGE_ID} not fully defined in matrix" >&2
  exit 1
fi

# Read the live IP before any mutation so the verification gate is honest.
old_ip="$(aws lightsail get-static-ip --region "$region" --static-ip-name "$static_ip_name" \
  --query 'staticIp.ipAddress' --output text 2>/dev/null || true)"
if [[ -z "$old_ip" || "$old_ip" == "None" ]]; then
  echo "::error::no live Static IP attached for ${EDGE_ID} (run provision first)" >&2
  exit 1
fi

ts="$(date -u +%Y%m%dT%H%M%SZ)"
new_name="${static_ip_name}-rot-${ts}"

cat <<PLAN
=== Lightsail Edge IP rotation plan ===
edge_id           : ${EDGE_ID}
lightsail_region  : ${region}
instance_name     : ${instance_name}
domain            : ${domain}
old_static_ip_name: ${static_ip_name}
old_ip            : ${old_ip}
new_static_ip_name: ${new_name}
ssm_prefix        : ${ssm_prefix}

Steps (each is irreversible without re-allocation):
  1) allocate Static IP ${new_name}
  2) detach ${static_ip_name} (=> ${old_ip}) from ${instance_name}
  3) attach ${new_name} to ${instance_name}; read NEW ip
  4) write ${ssm_prefix}/public_ip = NEW ip
  5) release ${static_ip_name}
  6) human gate: update Porkbun A record ${domain} -> NEW ip

PLAN

if [[ "$APPLY" != "--apply" ]]; then
  echo "(dry run — pass --apply to execute)"
  exit 0
fi

echo "[1/5] allocate ${new_name}"
aws lightsail allocate-static-ip --region "$region" --static-ip-name "$new_name" >/dev/null

echo "[2/5] detach ${static_ip_name}"
aws lightsail detach-static-ip --region "$region" --static-ip-name "$static_ip_name" >/dev/null

echo "[3/5] attach ${new_name} to ${instance_name}"
aws lightsail attach-static-ip --region "$region" --static-ip-name "$new_name" \
  --instance-name "$instance_name" >/dev/null

new_ip="$(aws lightsail get-static-ip --region "$region" --static-ip-name "$new_name" \
  --query 'staticIp.ipAddress' --output text)"
if [[ -z "$new_ip" || "$new_ip" == "None" ]]; then
  echo "::error::failed to read NEW Static IP after attach; aborting before release" >&2
  exit 1
fi
echo "  new_ip = ${new_ip}"

if [[ -n "$ssm_prefix" ]]; then
  echo "[4/5] write ${ssm_prefix}/public_ip = ${new_ip}"
  aws ssm put-parameter --region "$region" --name "${ssm_prefix}/public_ip" \
    --type String --value "$new_ip" --overwrite >/dev/null
fi

echo "[5/5] release ${static_ip_name}"
aws lightsail release-static-ip --region "$region" --static-ip-name "$static_ip_name" >/dev/null

cat <<DONE

=== rotation applied ===
edge_id : ${EDGE_ID}
old_ip  : ${old_ip}
new_ip  : ${new_ip}
domain  : ${domain}

NEXT (human):
  - Porkbun A record ${domain} -> ${new_ip}
  - external probe from a clean-egress host:
      curl -sS --resolve ${domain}:443:${new_ip} https://${domain}/health
  - after DNS propagation:
      gh workflow run deploy-edge-lightsail-stage0.yml \\
        -f edge_id=${EDGE_ID} -f operation=smoke \\
        -f confirm_instance=${instance_name}
DONE
